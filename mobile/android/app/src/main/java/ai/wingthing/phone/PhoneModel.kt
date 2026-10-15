package ai.wingthing.phone

import android.app.Application
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import bridge.Bridge
import bridge.Connection
import bridge.Enrollment
import kotlinx.coroutines.*
import org.json.JSONArray
import org.json.JSONObject

// All protocol work runs on one dispatcher. A failed command closes the connection;
// reconnect is explicit and input is never replayed.
class PhoneModel(app: Application) : AndroidViewModel(app) {
    private val secrets = Secrets(app)
    private val dispatcher = Dispatchers.IO.limitedParallelism(1)
    var state by mutableStateOf(PhoneState())
        private set
    private var enrollment: Enrollment? = null
    private var pendingAlias: String? = null
    private var wire: PhoneWire? = null
    private var connection: Connection? = null
    private var polling: Job? = null
    private var epoch = 0
    init { run { state = state.copy(wings = secrets.load()) } }
    private fun run(action: suspend () -> Unit) {
        viewModelScope.launch {
            state = state.copy(busy = true)
            try { action() }
            catch (e: Exception) {
                if (e is CancellationException) throw e
                failure(e)
            } finally { state = state.copy(busy = false) }
        }
    }
    private fun failure(e: Exception) {
        polling?.cancel(); polling = null
        connection = null
        wire?.close(); wire = null
        val alias = pendingAlias; pendingAlias = null; enrollment = null
        if (alias != null) viewModelScope.launch(dispatcher) { secrets.deleteKey(alias) }
        val message = e.message.orEmpty()
        val code = listOf("expired", "used", "cancelled", "identity_mismatch", "emoji_mismatch", "revoked", "bad_proof", "unreachable", "invalid_qr", "session_unavailable", "name_in_use").firstOrNull { message.contains(it) } ?: "denied"
        state = state.copy(screen = "error", error = code, terminal = "", busy = false)
    }
    fun scan() { if (state.busy) return; polling?.cancel(); epoch++; run { dispose(); state = state.copy(screen = "scan", error = "", terminal = "") } }
    fun permissionDenied() { state = state.copy(screen = "error", error = "local_permission") }
    fun fallback() { state = state.copy(screen = "fallback") }
    fun pair(link: String, words: String = "") {
        if (state.busy) return
        epoch++; val token = epoch
        run {
            dispose()
            val actual = withContext(dispatcher) { if (words.isBlank()) link else Bridge.fallback(link, words) }
            val metadata = JSONObject(withContext(dispatcher) { Bridge.parseInvitation(actual) })
            state = state.copy(screen = "pairing", machine = metadata.getString("name"), fingerprint = Bridge.fingerprint(metadata.getString("key")))
            val alias = withContext(dispatcher) { secrets.newSigningKey().also { pendingAlias = it } }
            val nextWire = PhoneWire(getApplication(), metadata); wire = nextWire
            withContext(dispatcher) { enrollment = Bridge.begin(actual, "${android.os.Build.MODEL} phone", secrets.publicKey(alias), nextWire) }
            if (token == epoch) state = state.copy(screen = "emoji", emoji = enrollment!!.emoji(), confirmationLabels = enrollment!!.confirmationLabels())
        }
    }
    fun confirm() {
        if (state.busy) return
        run {
            val pending = enrollment ?: error("cancelled")
            state = state.copy(screen = "waiting")
            var profile = withContext(dispatcher) { pending.confirm(state.emoji) }
            while (profile.isEmpty()) { delay(750); profile = withContext(dispatcher) { pending.status() } }
            val wing = SavedWing(profile, pendingAlias ?: error("bad_key"))
            val wings = state.wings.filterNot { it.id == wing.id } + wing
            withContext(dispatcher) { secrets.save(wings) }
            val replaced = state.wings.firstOrNull { it.id == wing.id }
            if (replaced != null) withContext(dispatcher) { secrets.deleteKey(replaced.alias) }
            pendingAlias = null; enrollment = null; wire?.close(); wire = null
            state = state.copy(wings = wings, screen = "machines", emoji = "")
        }
    }
    fun cancelPair() {
        epoch++; polling?.cancel()
        // Waiting is a cancellable coroutine; stop it by retaining an explicit job below.
        viewModelScope.coroutineContext.cancelChildren()
        state = state.copy(busy = false)
        run { dispose(); state = state.copy(screen = "error", error = "emoji_mismatch", terminal = "", emoji = "") }
    }
    private suspend fun dispose() {
        withContext(dispatcher) {
            try { enrollment?.cancel() } catch (_: Exception) { }
            enrollment = null
            try { connection?.command("close", "", "") } catch (_: Exception) { }
            connection = null
            wire?.close(); wire = null
            pendingAlias?.let { secrets.deleteKey(it) }; pendingAlias = null
        }
    }
    fun machines() {
        if (state.busy) return
        polling?.cancel(); epoch++
        run { dispose(); state = state.copy(screen = "machines", terminal = "", error = "") }
    }
    fun open(wing: SavedWing) {
        if (state.busy) return
        polling?.cancel(); epoch++
        run {
            dispose(); state = state.copy(machine = wing.name, selected = wing, screen = "connecting", terminal = "")
            val nextWire = PhoneWire(getApplication(), wing.json); wire = nextWire
            withContext(dispatcher) { connection = Bridge.connect(wing.profile, secrets.signer(wing.alias), nextWire) }
            val updated = SavedWing(connection!!.profile(), wing.alias)
            val wings = state.wings.map { if (it.id == wing.id) updated else it }
            withContext(dispatcher) { secrets.save(wings) }
            state = state.copy(selected = updated, wings = wings, route = nextWire.route)
            refreshSessions()
            startInventoryPolling()
        }
    }
    private suspend fun command(op: String, id: String = "", input: String = ""): String = withContext(dispatcher) {
        (connection ?: error("revoked")).command(op, id, input)
    }
    private suspend fun refreshSessions() {
        val array = JSONArray(command("sessions"))
        val sessions = (0 until array.length()).map {
            val j = array.getJSONObject(it)
            PhoneSession(j.getString("id"), j.optString("name").ifBlank { j.getString("id") }, j.optString("agent"), j.optString("status"), j.optBoolean("blocked"), j.optBoolean("unread"))
        }
        state = state.copy(screen = "sessions", sessions = sessions)
    }
    private fun startInventoryPolling() {
        val token = epoch
        polling = viewModelScope.launch {
            try { while (isActive && token == epoch && state.screen == "sessions") { delay(3000); refreshSessions() } }
            catch (e: Exception) { if (e !is CancellationException) failure(e) }
        }
    }
    fun attach(session: PhoneSession) {
        if (state.busy) return
        polling?.cancel(); epoch++; val token = epoch
        run {
            command("attach", session.id)
            state = state.copy(screen = "terminal", session = session, ended = false, terminal = "", terminalRevision = state.terminalRevision + 1)
            polling = viewModelScope.launch {
                try {
                    while (isActive && token == epoch) {
                        val read = JSONObject(command("read", session.id))
                        if (token != epoch) break
                        val output = read.optString("output")
                        // Raw output includes ANSI. ScreenText is plain text for truncated recovery.
                        val screen = read.optString("screen")
                        state = if (read.optBoolean("truncated") && screen.isNotEmpty()) state.copy(terminal = "\u001b[2J\u001b[H" + screen.replace("\n", "\r\n"), terminalRevision = state.terminalRevision + 1)
                        else state.copy(terminal = (state.terminal + output).takeLast(200000))
                        if (read.optBoolean("ended")) { state = state.copy(ended = true); break }
                        delay(500)
                    }
                } catch (e: Exception) { if (e !is CancellationException) failure(e) }
            }
        }
    }
    fun input(text: String) { if (state.busy || state.ended) return; run { command("input", state.session?.id ?: error("denied"), text) } }
    fun stopSession() {
        if (state.busy) return
        polling?.cancel(); epoch++
        run { command("stop", state.session?.id ?: error("denied")); state = state.copy(ended = true) }
    }
    fun detach() {
        if (state.busy) return
        polling?.cancel(); epoch++
        run { command("detach", state.session?.id ?: ""); state = state.copy(terminal = "", ended = false); refreshSessions(); startInventoryPolling() }
    }
    fun manage(wing: SavedWing? = state.selected) { polling?.cancel(); if (wing != null) state = state.copy(screen = "manage", selected = wing, machine = wing.name) }
    fun forget() {
        if (state.busy) return
        run {
            val wing = state.selected ?: error("denied"); dispose()
            val wings = state.wings.filterNot { it.id == wing.id }
            withContext(dispatcher) { secrets.save(wings); secrets.deleteKey(wing.alias) }
            state = state.copy(screen = "machines", wings = wings, selected = null, terminal = "", sessions = emptyList())
        }
    }
    fun background() { polling?.cancel(); epoch++; viewModelScope.coroutineContext.cancelChildren(); run { dispose(); state = state.copy(screen = "machines", busy = false, terminal = "", sessions = emptyList()) } }
    override fun onCleared() { wire?.close(); super.onCleared() }
}
data class PhoneSession(val id: String, val name: String, val agent: String, val status: String, val blocked: Boolean, val unread: Boolean)
data class PhoneState(
    val screen: String = "machines", val wings: List<SavedWing> = emptyList(), val selected: SavedWing? = null,
    val confirmationLabels: String = "",
    val machine: String = "your computer", val fingerprint: String = "", val emoji: String = "", val route: String = "",
    val sessions: List<PhoneSession> = emptyList(), val session: PhoneSession? = null,
    val terminal: String = "", val terminalRevision: Int = 0, val ended: Boolean = false, val busy: Boolean = false, val error: String = ""
)
fun failureCopy(code: String, name: String): Pair<String, String> = when (code) {
    "expired", "used" -> "Scan a fresh QR" to "This QR expired or was already used. On the computer, press Enter for a fresh QR."
    "identity_mismatch" -> "Identity mismatch" to "$name’s fingerprint does not match the QR. Pairing stopped. Check the computer before scanning a fresh QR."
    "emoji_mismatch", "cancelled" -> "Pairing cancelled" to "The verification words did not match, or pairing was cancelled. No access granted. Cancel on the computer and get a fresh QR."
    "unreachable" -> "Can’t reach $name" to "Check that your phone and computer are on the same Wi-Fi, or both connected to Tailscale. For remote access through Wingthing, run wt login once on the computer."
    "revoked" -> "Access revoked" to "This phone can no longer connect to $name. Scan a fresh QR to pair again."
    "local_permission" -> "Allow nearby-device access" to "Wingthing needs permission to connect to your computer on Wi-Fi. Open Android Settings, choose Apps, Wingthing, Permissions, and allow nearby-device access. Then come back and scan again."
    "name_in_use" -> "This phone is already paired" to "On the computer, run wt devices and revoke the old pairing for this phone. Then get a fresh QR and pair again."
    "invalid_qr" -> "That QR won’t work" to "Scan the Wingthing QR from wt pair on your computer."
    "session_unavailable" -> "Session unavailable" to "This session may have ended. Go back to your machines and connect again."
    else -> "Connection stopped" to "The private connection check failed. Check the computer, then reconnect or scan a fresh QR. Anything you typed will not be sent again."
}
