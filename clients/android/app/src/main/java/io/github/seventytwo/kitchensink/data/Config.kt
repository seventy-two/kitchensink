// The app's settings, kept as config.json in its private storage, like the
// Windows client's. The token is encrypted with a key in the Android
// Keystore, so it can't be read from a backup or another app.
package io.github.seventytwo.kitchensink.data

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import io.github.seventytwo.kitchensink.model.Cleared
import io.github.seventytwo.kitchensink.model.Priority
import java.io.File
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/** OLED care guards OLED screens against burn-in, each part on its own. */
@Serializable
data class OledCare(
    /** Moves everything round a small circle every three minutes. */
    val shift: Boolean = false,
    /** A true-black theme with dimmer text and fainter borders. */
    val black: Boolean = false,
    /** Dims after ten minutes without a touch or key press. */
    val dim: Boolean = false,
    /** Swaps the Recent column's side every other hour. */
    val swap: Boolean = false,
)

@Serializable
enum class TvMode { Auto, On, Off }

@Serializable
enum class ThemeChoice { System, Light, Dark }

@Serializable
data class Config(
    /** e.g. https://example.com/kitchensink */
    val url: String = "",
    /** Kept encrypted on disk; see [ConfigStore]. */
    @kotlinx.serialization.Transient val token: String = "",
    val tokenSealed: String = "",
    /** Events below this priority don't raise a notification. */
    val minPriority: Priority = Priority.Normal,
    val notify: Boolean = true,
    /** Stay connected while the app's in the background, for notifications as things happen. */
    val background: Boolean = true,
    val oled: OledCare = OledCare(),
    /** A black cat asleep above the Recent column. */
    val cat: Boolean = false,
    val clearedThrough: Long = 0,
    val clearedIds: List<Long> = emptyList(),
    /** Hides private figures, e.g. a portfolio's value. */
    val hidePrivate: Boolean = false,
    /** In the compact layout, monitors opened or closed by hand. The rest open by themselves while there's room. */
    val expanded: Set<String> = emptySet(),
    val collapsed: Set<String> = emptySet(),
    /** Panel order; empty for the server's config order. */
    val order: List<String> = emptyList(),
    /** TV mode: an always-on dashboard for a TV or a screen on the wall. Auto is on for Android TV. */
    val tv: TvMode = TvMode.Auto,
    val theme: ThemeChoice = ThemeChoice.System,
) {
    val cleared: Cleared get() = Cleared(clearedThrough, clearedIds.toSet())
    fun withCleared(c: Cleared) = copy(clearedThrough = c.through, clearedIds = c.ids.sorted())
    val configured: Boolean get() = url.isNotBlank() && token.isNotBlank()
}

/** Reads and writes [Config]. */
class ConfigStore(context: Context) {
    private val file = File(context.filesDir, "config.json")
    private val format = Json { ignoreUnknownKeys = true; encodeDefaults = true; prettyPrint = true }

    /** The settings, with defaults for anything missing or unreadable. */
    fun load(): Config {
        val c = try {
            format.decodeFromString<Config>(file.readText())
        } catch (_: Exception) {
            return Config()
        }
        return c.copy(token = unseal(c.tokenSealed))
    }

    fun save(c: Config) {
        val sealed = if (c.token.isEmpty()) "" else seal(c.token)
        val tmp = File(file.path + ".tmp")
        tmp.writeText(format.encodeToString(Config.serializer(), c.copy(tokenSealed = sealed)))
        tmp.renameTo(file)
    }

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(KEY, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(KEY, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .build(),
        )
        return gen.generateKey()
    }

    private fun seal(plain: String): String = try {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val out = cipher.iv + cipher.doFinal(plain.toByteArray())
        Base64.encodeToString(out, Base64.NO_WRAP)
    } catch (_: Exception) {
        ""
    }

    private fun unseal(sealed: String): String = try {
        if (sealed.isEmpty()) "" else {
            val raw = Base64.decode(sealed, Base64.NO_WRAP)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, raw, 0, 12))
            String(cipher.doFinal(raw, 12, raw.size - 12))
        }
    } catch (_: Exception) {
        ""
    }

    private companion object {
        const val KEY = "kitchensink-token"
    }
}
