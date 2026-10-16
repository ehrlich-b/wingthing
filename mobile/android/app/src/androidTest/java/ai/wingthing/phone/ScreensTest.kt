package ai.wingthing.phone

import androidx.activity.ComponentActivity
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.By
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class ScreensTest {
    @get:Rule val compose = createAndroidComposeRule<ComponentActivity>()
    @Test fun everyScreenInLightAndDarkAndDestructiveActionsNeedConfirmation() {
        val profile = "{\"wing_id\":\"fixture-wing\",\"name\":\"Studio Mac\"}"
        val mac = SavedWing(profile, "fixture")
        val devbox = SavedWing("{\"wing_id\":\"fixture-devbox\",\"name\":\"devbox\"}", "fixture")
        val base = PhoneState(wings = listOf(mac, devbox), selected = mac, machine = "Studio Mac", confirmationLabels = "tree · octopus · bicycle · moon",
            fingerprint = "66687aadf862bd776c8fc18b8e9f8e20089714856ee233b3902a591d0d5f2925",
            route = "Connected on your network · Encrypted",
            sessions = listOf(PhoneSession("1", "QR pairing", "Codex", "working", false, true), PhoneSession("2", "Release notes", "Claude", "blocked", true, false), PhoneSession("3", "Shell", "shell", "idle", false, false)),
            session = PhoneSession("1", "QR pairing", "Codex", "working", false, true),
            terminal = "\u001b[32mWingthing · Studio Mac\u001b[0m\r\n\r\nWorking on your request…\r\n\u001b[33mReady for your next message.\u001b[0m\r\n❯ ")
        var state by mutableStateOf(base)
        var dark by mutableStateOf(false)
        val actions = mutableListOf<String>()
        compose.setContent { WingthingTheme(dark) { PhoneApp(state, { action, _ -> actions.add(action) }) } }
        val device = UiDevice.getInstance(InstrumentationRegistry.getInstrumentation())
        fun capture(name: String) {
            compose.waitForIdle()
            device.waitForIdle(1000)
            // Compose idleness precedes SurfaceFlinger's displayed frame.
            // Allow that frame to reach the display before the system capture.
            android.os.SystemClock.sleep(500)
            assertFalse("System ANR dialog obscures $name", device.hasObject(By.textContains("isn't responding")))
            captureEvidence(device, InstrumentationRegistry.getInstrumentation().targetContext, name)
        }
        for (theme in listOf("light", "dark")) {
            compose.runOnIdle { dark = theme == "dark" }
            for (screen in listOf("machines", "scan", "fallback", "pairing", "emoji", "waiting", "connecting", "sessions", "terminal", "manage")) {
                compose.runOnIdle { state = base.copy(screen = screen) }
                capture("$screen-$theme")
                if (screen == "sessions") {
                    compose.onNodeWithText("Needs you").assertIsDisplayed()
                    compose.onNodeWithText("Unread").assertIsDisplayed()
                }
                if (screen == "terminal") {
                    compose.onNodeWithText("Stop", substring = false).performClick()
                    compose.onNodeWithText("Stop this session?").assertIsDisplayed()
                    assertFalse(actions.contains("stop"))
                    capture("stop-confirm-$theme")
                    compose.onNodeWithText("Keep running").performClick()
                }
                if (screen == "manage") {
                    compose.onNodeWithText("Forget Studio Mac").performScrollTo().performClick()
                    compose.onNodeWithText("Forget Studio Mac?").assertIsDisplayed()
                    assertFalse(actions.contains("forget"))
                    capture("forget-confirm-$theme")
                    compose.onNodeWithText("Keep pairing").performClick()
                }
            }
            compose.runOnIdle { state = base.copy(screen = "machines", wings = emptyList()) }; capture("empty-$theme")
            compose.runOnIdle { state = base.copy(screen = "sessions", sessions = emptyList()) }; capture("sessions-empty-$theme")
            compose.runOnIdle { state = base.copy(screen = "terminal", ended = true) }; capture("stopped-$theme")
            for (error in listOf("expired", "unreachable", "identity_mismatch", "emoji_mismatch", "revoked", "invalid_qr", "session_unavailable", "bad_proof", "name_in_use", "local_permission")) {
                compose.runOnIdle { state = base.copy(screen = "error", error = error) }
                capture("$error-$theme")
                if (error == "identity_mismatch") compose.onNodeWithText("Continue").assertDoesNotExist()
            }
        }
    }
    @Test fun ansiParserMovesCursorAndPreservesColor() {
        compose.runOnUiThread {
            val view = RemoteTerminalView(compose.activity)
            view.show("hello\r\n\u001b[31mred\u001b[0m\u001b[2;1HOK", 1)
            val selected = view.emulator.getSelectedText(0, 0, 79, 23)
            assertTrue(selected.contains("hello")); assertTrue(selected.contains("OKd"))
        }
    }
}
