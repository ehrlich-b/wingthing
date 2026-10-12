package ai.wingthing.phone

import android.app.Activity
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.view.WindowManager
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.compose.foundation.background
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalView
import androidx.core.view.WindowInsetsControllerCompat
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions

class MainActivity : ComponentActivity() {
    val model: PhoneModel by viewModels()
    private var permissionAction: (() -> Unit)? = null
    private val nearby = registerForActivityResult(ActivityResultContracts.RequestPermission()) { allowed ->
        if (allowed) permissionAction?.invoke() else model.permissionDenied()
        permissionAction = null
    }
    private val scanner = registerForActivityResult(ScanContract()) { result -> result.contents?.let { model.pair(it) } }
    private fun localPermission(action: () -> Unit) {
        if (Build.VERSION.SDK_INT >= 37 && checkSelfPermission("android.permission.ACCESS_LOCAL_NETWORK") != PackageManager.PERMISSION_GRANTED) {
            permissionAction = action; nearby.launch("android.permission.ACCESS_LOCAL_NETWORK")
        } else action()
    }
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        setContent {
            WingthingTheme {
                PhoneApp(model.state, onAction = { action, value ->
                    when (action) {
                        "scan" -> model.scan()
                        "camera" -> localPermission { scanner.launch(ScanOptions().setCaptureActivity(ScannerActivity::class.java).setDesiredBarcodeFormats(ScanOptions.QR_CODE).setPrompt("Scan the QR from wt pair on your computer").setBeepEnabled(false).setOrientationLocked(false)) }
                        "fallback" -> model.fallback()
                        "pair" -> localPermission { model.pair(value.substringBefore('\n'), value.substringAfter('\n', "")) }
                        "confirm" -> model.confirm()
                        "cancel" -> model.cancelPair()
                        "machines" -> model.machines()
                        "open" -> model.state.wings.firstOrNull { it.id == value }?.let { wing -> localPermission { model.open(wing) } }
                        "attach" -> model.state.sessions.firstOrNull { it.id == value }?.let { model.attach(it) }
                        "input" -> model.input(value)
                        "stop" -> model.stopSession()
                        "detach" -> model.detach()
                        "manage" -> model.manage(model.state.wings.firstOrNull { it.id == value } ?: model.state.selected)
                        "forget" -> model.forget()
                    }
                })
            }
        }
    }
    override fun onStop() { super.onStop(); if (!isChangingConfigurations) model.background() }
}
@Composable fun WingthingTheme(dark: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    val view = LocalView.current
    if (!view.isInEditMode) SideEffect {
        (view.context as? Activity)?.window?.let { window ->
            WindowInsetsControllerCompat(window, view).apply {
                isAppearanceLightStatusBars = !dark
                isAppearanceLightNavigationBars = !dark
            }
        }
    }
    val colors = if (dark) darkColorScheme(primary = Color(0xff8ee9c4), background = Color(0xff121a18), surface = Color(0xff1e2a25), onSurface = Color(0xffe6efeb))
    else lightColorScheme(primary = Color(0xff006d57), background = Color(0xfff4f4ee), surface = Color.White, onSurface = Color(0xff192624))
    MaterialTheme(colorScheme = colors, content = content)
}
@Composable fun PhoneApp(state: PhoneState, onAction: (String, String) -> Unit) {
    var stopDialog by remember { mutableStateOf(false) }
    var forgetDialog by remember { mutableStateOf(false) }
    var text by remember(state.screen, state.session?.id) { mutableStateOf("") }
    var link by remember { mutableStateOf("") }
    var words by remember { mutableStateOf("") }
    fun action(name: String, value: String = "") = onAction(name, value)
    BackHandler(state.screen != "machines") {
        when (state.screen) { "terminal" -> action("detach"); "emoji", "waiting", "pairing" -> action("cancel"); else -> action("machines") }
    }
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
        Column(Modifier.fillMaxSize().safeDrawingPadding().imePadding().padding(horizontal = 22.dp, vertical = 16.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
            Text("WINGTHING", color = MaterialTheme.colorScheme.primary, style = MaterialTheme.typography.labelLarge)
            val title = when (state.screen) {
                "scan" -> "Scan to pair"
                "fallback" -> "Use the pairing code"
                "emoji" -> "Pair with ${state.machine}?"
                "pairing" -> "Pairing with ${state.machine}"
                "waiting" -> "Check the computer"
                "connecting" -> "Connecting to ${state.machine}"
                "sessions" -> state.machine
                "terminal" -> state.session?.name ?: "Session"
                "manage" -> "This pairing"
                "error" -> failureCopy(state.error, state.machine).first
                else -> "Your machines"
            }
            Text(title, style = MaterialTheme.typography.headlineMedium)
            if (state.busy) LinearProgressIndicator(Modifier.fillMaxWidth())
            if (state.screen == "terminal") {
                Text(state.route, style = MaterialTheme.typography.bodySmall)
                AndroidView(factory = { RemoteTerminalView(it) }, modifier = Modifier.fillMaxWidth().weight(1f), update = { it.show(state.terminal, state.terminalRevision) })
                if (state.ended) Text("This session has stopped. You can go back to your sessions.")
                else {
                    OutlinedTextField(text, { text = it }, label = { Text("Type a message or command") }, modifier = Modifier.fillMaxWidth(), enabled = !state.busy)
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        Button({ action("input", text + "\r"); text = "" }, enabled = !state.busy && text.isNotEmpty()) { Text("Send ↵") }
                        OutlinedButton({ action("input", "\u0003") }, enabled = !state.busy) { Text("Ctrl+C") }
                        OutlinedButton({ action("input", "\t") }, enabled = !state.busy) { Text("Tab") }
                    }
                }
                Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    OutlinedButton({ action("detach") }, enabled = !state.busy) { Text("Back to sessions") }
                    TextButton({ stopDialog = true }, enabled = !state.busy && !state.ended) { Text("Stop", color = MaterialTheme.colorScheme.error) }
                }
            } else Column(Modifier.weight(1f).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(18.dp)) {
                when (state.screen) {
                    "machines" -> {
                        Text("Your work stays on each computer. Pair each one separately.")
                        if (state.wings.isEmpty()) Card { Column(Modifier.padding(22.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
                            Text("Start with your computer", style = MaterialTheme.typography.titleLarge)
                            Text("On your computer, run wt pair. Then scan the QR here and check the emoji on both screens.")
                            Text("No account needed on your phone.", color = MaterialTheme.colorScheme.primary)
                        } }
                        state.wings.forEach { wing -> Card(onClick = { action("open", wing.id) }, enabled = !state.busy, modifier = Modifier.fillMaxWidth()) {
                            Column(Modifier.padding(20.dp)) { Text(wing.name, style = MaterialTheme.typography.titleLarge); Text("Paired · Tap to see sessions"); TextButton({ action("manage", wing.id) }) { Text("Manage pairing") } }
                        } }
                        Button({ action("scan") }, enabled = !state.busy, modifier = Modifier.fillMaxWidth()) { Text(if (state.wings.isEmpty()) "Pair a computer" else "Scan another wing") }
                    }
                    "scan" -> {
                        Text("On your computer, run wt pair. Point your camera at the QR in the terminal.")
                        Box(Modifier.fillMaxWidth().height(220.dp).background(MaterialTheme.colorScheme.surfaceVariant), contentAlignment = Alignment.Center) {
                            Column(horizontalAlignment = Alignment.CenterHorizontally) { Text("⌜                     ⌝", fontSize = 36.sp); Text("QR on your computer", modifier = Modifier.padding(24.dp)); Text("⌞                     ⌟", fontSize = 36.sp) }
                        }
                        Button({ action("camera") }, modifier = Modifier.fillMaxWidth()) { Text("Open camera") }
                        Text("Allow camera and nearby-device access when asked. This lets Wingthing read the QR and connect to your computer.")
                        OutlinedButton({ action("fallback") }, modifier = Modifier.fillMaxWidth()) { Text("Use a pairing link and six-word code") }
                        TextButton({ action("machines") }) { Text("Back to machines") }
                    }
                    "fallback" -> {
                        Text("If scanning is difficult, paste the pairing link from your computer. The link tells us which computer to find; the six words confirm it’s yours.")
                        OutlinedTextField(link, { link = it }, label = { Text("Pairing link") }, modifier = Modifier.fillMaxWidth(), maxLines = 4)
                        OutlinedTextField(words, { words = it }, label = { Text("Six words shown on the computer") }, modifier = Modifier.fillMaxWidth())
                        Button({ action("pair", link.trim() + "\n" + words.trim()) }, enabled = !state.busy && link.isNotBlank(), modifier = Modifier.fillMaxWidth()) { Text("Check this computer") }
                        TextButton({ action("scan") }) { Text("Back to scan") }
                    }
                    "emoji" -> {
                        Text("See and control sessions on this computer.")
                        Card { Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                            Text(state.emoji, fontSize = 30.sp)
                            Text("Do all four emoji match the computer?", style = MaterialTheme.typography.titleMedium)
                            Text("Only approve if both screens show exactly the same emoji.")
                        } }
                        Text("Wing fingerprint", style = MaterialTheme.typography.labelLarge)
                        Text(state.fingerprint, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
                        Button({ action("confirm") }, enabled = !state.busy, modifier = Modifier.fillMaxWidth()) { Text("Emoji match · Pair") }
                        OutlinedButton({ action("cancel") }, modifier = Modifier.fillMaxWidth()) { Text("Cancel · emoji differ") }
                    }
                    "waiting" -> {
                        Text(state.emoji, fontSize = 30.sp)
                        Text("Now press Enter in the computer’s terminal to allow this phone. Both screens need your approval.")
                        Text("Your private key stays on this phone.")
                        OutlinedButton({ action("cancel") }, modifier = Modifier.fillMaxWidth()) { Text("Cancel pairing") }
                    }
                    "pairing", "connecting" -> { Text("Making a private connection…"); Text("Your sessions stay on ${state.machine}.") }
                    "sessions" -> {
                        Text(state.route, color = MaterialTheme.colorScheme.primary)
                        Text("This computer only. Tap a session to open it.")
                        if (state.sessions.isEmpty()) Text("No sessions yet. Start some work on this computer, then it will appear here.")
                        state.sessions.forEach { session -> Card(onClick = { action("attach", session.id) }, enabled = !state.busy, modifier = Modifier.fillMaxWidth()) {
                            Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                Text(session.name, style = MaterialTheme.typography.titleLarge)
                                Text("${session.agent} · ${session.status}")
                                Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                                    if (session.blocked) Text("Needs you", color = MaterialTheme.colorScheme.primary, style = MaterialTheme.typography.labelLarge)
                                    if (session.unread) Text("Unread", color = MaterialTheme.colorScheme.primary, style = MaterialTheme.typography.labelLarge)
                                }
                            }
                        } }
                        OutlinedButton({ action("machines") }, enabled = !state.busy) { Text("All machines") }
                        TextButton({ action("manage") }) { Text("Manage this pairing") }
                    }
                    "manage" -> {
                        Text(state.machine, style = MaterialTheme.typography.titleLarge)
                        Text("To revoke this phone’s access, on this computer run:")
                        Text("wt devices", fontFamily = FontFamily.Monospace)
                        Text("Find this phone’s name or device ID, then run:")
                        Text("wt devices revoke \"${state.selected?.json?.optString("device_id")?.ifBlank { "<device-id>" } ?: "<device-id>"}\"", fontFamily = FontFamily.Monospace)
                        Text("This closes live access immediately. Repeat on every paired computer if your phone is lost.")
                        Text("Forget removes this pairing from this phone. To connect again, you’ll need a fresh QR.")
                        OutlinedButton({ forgetDialog = true }, enabled = !state.busy, modifier = Modifier.fillMaxWidth()) { Text("Forget ${state.machine}") }
                        TextButton({ action("machines") }) { Text("Back to machines") }
                    }
                    "error" -> {
                        Text(failureCopy(state.error, state.machine).second)
                        if (state.error == "identity_mismatch") Text("Check the computer before trying again.", color = MaterialTheme.colorScheme.error)
                        Button({ action("scan") }, modifier = Modifier.fillMaxWidth()) { Text("Scan a fresh QR") }
                        TextButton({ action("machines") }) { Text("Back to machines") }
                    }
                }
            }
        }
    }
    if (stopDialog) AlertDialog(onDismissRequest = { stopDialog = false }, title = { Text("Stop this session?") }, text = { Text("This ends the work running in this session on ${state.machine}.") }, confirmButton = { TextButton({ stopDialog = false; action("stop") }) { Text("Stop session") } }, dismissButton = { TextButton({ stopDialog = false }) { Text("Keep running") } })
    if (forgetDialog) AlertDialog(onDismissRequest = { forgetDialog = false }, title = { Text("Forget ${state.machine}?") }, text = { Text("This removes its saved access from this phone. Revoke on the computer too if you want to end the grant there.") }, confirmButton = { TextButton({ forgetDialog = false; action("forget") }) { Text("Forget") } }, dismissButton = { TextButton({ forgetDialog = false }) { Text("Keep pairing") } })
}
