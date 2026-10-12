package ai.wingthing.phone

import android.Manifest
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.rule.GrantPermissionRule
import bridge.Bridge
import bridge.Wire
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.net.HttpURLConnection
import java.net.URL

@RunWith(AndroidJUnit4::class)
class InteropTest {
    @get:Rule val permissions: GrantPermissionRule = GrantPermissionRule.grant(*(if (android.os.Build.VERSION.SDK_INT >= 37) arrayOf("android.permission.ACCESS_LOCAL_NETWORK") else emptyArray()))
    private val context get() = InstrumentationRegistry.getInstrumentation().targetContext
    private fun control(op: String, id: String = "", name: String = ""): JSONObject {
        val c = URL("http://127.0.0.1:38473/fixture").openConnection() as HttpURLConnection
        c.requestMethod = "POST"; c.doOutput = true; c.setRequestProperty("Content-Type", "application/json")
        c.connectTimeout = 5000; c.readTimeout = 5000
        c.outputStream.use { it.write(JSONObject().put("Op", op).put("ID", id).put("Name", name).toString().toByteArray()) }
        return c.inputStream.bufferedReader().use { JSONObject(it.readText()) }.also { c.disconnect() }
    }
    @Test fun pairsWithGoPeerAndEncryptsCommandsThenRevokesLiveSession() {
        val link = control("create").getString("link")
        val metadata = JSONObject(Bridge.parseInvitation(link))
        val store = Secrets(context); val alias = store.newSigningKey()
        PhoneWire(context, metadata) { null }.use { wire ->
            try {
                val enrollment = Bridge.begin(link, "Android interop", store.publicKey(alias), wire)
                val status = control("status", metadata.getString("id"))
                assertEquals(status.getString("emoji"), enrollment.emoji())
                assertEquals("", enrollment.confirm(enrollment.emoji()))
                assertTrue(control("allow", metadata.getString("id")).getBoolean("ok"))
                val profile = enrollment.status()
                val connection = Bridge.connect(profile, store.signer(alias), PhoneWire(context, JSONObject(profile)) { null })
                val sessions = org.json.JSONArray(connection.command("sessions", "", ""))
                assertTrue(sessions.getJSONObject(0).getBoolean("blocked"))
                assertTrue(sessions.getJSONObject(0).getBoolean("unread"))
                assertTrue(JSONObject(connection.command("attach", "fixture-shell", "")).getBoolean("ok"))
                assertTrue(JSONObject(connection.command("input", "fixture-shell", "hello\r")).getBoolean("ok"))
                assertTrue(JSONObject(connection.command("read", "fixture-shell", "")).getString("output").contains("fixture output"))
                assertTrue(control("revoke", name = "Android interop").getBoolean("ok"))
                expectError("revoked") { connection.command("read", "fixture-shell", "") }
            } finally { store.deleteKey(alias) }
        }
    }
    @Test fun wrongCodeAndEmojiMismatchDoNotGrantAccess() {
        val store = Secrets(context); val alias = store.newSigningKey()
        try {
            val link = control("create").getString("link")
            val wrong = Bridge.fallback(link, "wrong wrong wrong wrong wrong wrong")
            PhoneWire(context, JSONObject(Bridge.parseInvitation(wrong))) { null }.use { wire ->
                expectError("bad_proof") { Bridge.begin(wrong, "Wrong code", store.publicKey(alias), wire) }
            }
            val fresh = control("create").getString("link")
            val metadata = JSONObject(Bridge.parseInvitation(fresh))
            PhoneWire(context, metadata) { null }.use { wire ->
                val enrollment = Bridge.begin(fresh, "Mismatch", store.publicKey(alias), wire)
                expectError("emoji_mismatch") { enrollment.confirm("wrong emoji") }
                assertEquals("cancelled", control("status", metadata.getString("id")).getString("state"))
            }
        } finally { store.deleteKey(alias) }
    }
    @Test fun expiryAndCarrierTamperHardStop() {
        val fresh = control("create").getString("link")
        val metadata = JSONObject(Bridge.parseInvitation(fresh))
        metadata.put("expires_at", "2020-01-01T00:00:00Z")
        val expired = "wingthing://pair#" + java.net.URLEncoder.encode(metadata.toString(), "UTF-8").replace("+", "%20")
        expectError("expired") { Bridge.parseInvitation(expired) }
        val store = Secrets(context); val alias = store.newSigningKey()
        try {
            val badWire = object : Wire { override fun exchange(box: String, initial: Boolean) = "{\"payload\":\"AAAA\"}" }
            expectError("bad_proof") { Bridge.begin(fresh, "Tamper", store.publicKey(alias), badWire) }
        } finally { store.deleteKey(alias) }
    }
    @Test fun wingEnforcesExpiryEvenWhenPhoneInvitationClaimsMoreTime() {
        val link = control("create").getString("link")
        val metadata = JSONObject(Bridge.parseInvitation(link))
        // Real five-minute protocol deadline, not an altered server policy.
        android.os.SystemClock.sleep(301000)
        metadata.put("expires_at", java.time.Instant.now().plusSeconds(300).toString())
        val refreshedClaim = "wingthing://pair#" + java.net.URLEncoder.encode(metadata.toString(), "UTF-8").replace("+", "%20")
        val store = Secrets(context); val alias = store.newSigningKey()
        try {
            PhoneWire(context, metadata) { null }.use { wire ->
                expectError("expired") { Bridge.begin(refreshedClaim, "Expired invitation", store.publicKey(alias), wire) }
            }
        } finally { store.deleteKey(alias) }
    }
    @Test fun credentialStorageIsEncryptedAndKeystoreSignaturesSurviveReload() {
        val store = Secrets(context); val alias = store.newSigningKey()
        try {
            val saved = SavedWing("{\"wing_id\":\"fixture-store\",\"name\":\"Mac\",\"credential\":\"credential-must-not-be-plaintext\"}", alias)
            val original = store.load()
            try {
                store.save(original + saved)
                val bytes = java.io.File(context.noBackupFilesDir, "wings.box").readBytes()
                assertFalse(String(bytes).contains("credential-must-not-be-plaintext"))
                assertEquals(saved, Secrets(context).load().last())
                val signature = store.signer(alias).sign("fixture challenge")
                val publicKey = java.security.KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.getCertificate(alias).publicKey
                val verifier = java.security.Signature.getInstance("SHA256withECDSA").apply { initVerify(publicKey); update("fixture challenge".toByteArray()) }
                assertTrue(verifier.verify(android.util.Base64.decode(signature, android.util.Base64.DEFAULT)))
            } finally { store.save(original) }
        } finally { store.deleteKey(alias) }
    }
    private fun expectError(code: String, action: () -> Any?) {
        try { action(); fail("Expected $code") } catch (e: Exception) { assertTrue("Expected $code, got ${e.message}", e.message.orEmpty().contains(code)) }
    }
}
