package ai.wingthing.phone

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.security.keystore.StrongBoxUnavailableException
import android.util.Base64
import bridge.Signer
import org.json.JSONArray
import org.json.JSONObject
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.util.UUID
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

class Secrets(private val context: Context) {
    private val keys = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    private val file get() = java.io.File(context.noBackupFilesDir, "wings.box")
    fun newSigningKey(): String {
        val alias = "wt-sign-${UUID.randomUUID()}"
        fun create(strong: Boolean) {
            val spec = KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_SIGN)
                .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                .setDigests(KeyProperties.DIGEST_SHA256)
            if (Build.VERSION.SDK_INT >= 28) spec.setIsStrongBoxBacked(strong)
            KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore").apply {
                initialize(spec.build()); generateKeyPair()
            }
        }
        if (Build.VERSION.SDK_INT >= 28 && context.packageManager.hasSystemFeature("android.hardware.strongbox_keystore")) {
            StrongBoxKeys.createOrFallback(::create)
        } else create(false)
        return alias
    }
    fun publicKey(alias: String): String {
        val pub = keys.getCertificate(alias).publicKey as ECPublicKey
        fun coordinate(n: java.math.BigInteger): ByteArray {
            val bytes = n.toByteArray()
            return ByteArray(32).also { bytes.copyInto(it, 32 - minOf(32, bytes.size), maxOf(0, bytes.size - 32)) }
        }
        return b64(byteArrayOf(4) + coordinate(pub.w.affineX) + coordinate(pub.w.affineY))
    }
    fun signer(alias: String) = object : Signer {
        override fun sign(text: String): String = b64(Signature.getInstance("SHA256withECDSA").run {
            initSign(keys.getKey(alias, null) as java.security.PrivateKey)
            update(text.toByteArray(Charsets.UTF_8)); sign()
        })
    }
    fun deleteKey(alias: String) { if (keys.containsAlias(alias)) keys.deleteEntry(alias) }
    private fun wrappingKey(): SecretKey {
        val alias = "wt-credential-wrap-v1"
        if (!keys.containsAlias(alias)) {
            KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").apply {
                init(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                    .setRandomizedEncryptionRequired(true).build())
                generateKey()
            }
        }
        return keys.getKey(alias, null) as SecretKey
    }
    @Synchronized fun load(): List<SavedWing> {
        if (!file.exists()) return emptyList()
        val bytes = file.readBytes()
        require(bytes.size in 28..(1024 * 1024))
        val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply {
            init(Cipher.DECRYPT_MODE, wrappingKey(), GCMParameterSpec(128, bytes.copyOfRange(0, 12)))
            updateAAD("wt-android-profiles-v1".toByteArray())
        }
        val array = JSONArray(String(cipher.doFinal(bytes.copyOfRange(12, bytes.size)), Charsets.UTF_8))
        return (0 until array.length()).map { SavedWing(array.getJSONObject(it).getString("profile"), array.getJSONObject(it).getString("alias")) }
    }
    @Synchronized fun save(wings: List<SavedWing>) {
        val array = JSONArray().apply { wings.forEach { put(JSONObject().put("profile", it.profile).put("alias", it.alias)) } }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply {
            init(Cipher.ENCRYPT_MODE, wrappingKey()); updateAAD("wt-android-profiles-v1".toByteArray())
        }
        val data = cipher.doFinal(array.toString().toByteArray(Charsets.UTF_8))
        val atomic = android.util.AtomicFile(file)
        val stream = atomic.startWrite()
        try { stream.write(cipher.iv + data); atomic.finishWrite(stream) }
        catch (e: Exception) { atomic.failWrite(stream); throw e }
    }
    private object StrongBoxKeys {
        @androidx.annotation.RequiresApi(28)
        fun createOrFallback(create: (Boolean) -> Unit) {
            try { create(true) } catch (_: StrongBoxUnavailableException) { create(false) }
        }
    }
    companion object { fun b64(bytes: ByteArray): String = Base64.encodeToString(bytes, Base64.NO_WRAP) }
}
data class SavedWing(val profile: String, val alias: String) {
    val json get() = JSONObject(profile)
    val id get() = json.getString("wing_id")
    val name get() = json.getString("name")
}
