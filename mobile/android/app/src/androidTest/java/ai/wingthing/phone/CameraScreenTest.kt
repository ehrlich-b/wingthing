package ai.wingthing.phone

import android.Manifest
import android.view.WindowManager
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.rule.GrantPermissionRule
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.By
import androidx.test.uiautomator.Until
import com.journeyapps.barcodescanner.ScanOptions
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class CameraScreenTest {
 @get:Rule val permissions: GrantPermissionRule = GrantPermissionRule.grant(Manifest.permission.CAMERA)
 @Test fun cameraScreenInBothThemes() {
  val context = InstrumentationRegistry.getInstrumentation().targetContext
  val device = UiDevice.getInstance(InstrumentationRegistry.getInstrumentation())
  for (theme in listOf("light", "dark")) {
   context.getSystemService(android.app.UiModeManager::class.java).setApplicationNightMode(if (theme == "dark") android.app.UiModeManager.MODE_NIGHT_YES else android.app.UiModeManager.MODE_NIGHT_NO)
   val intent = ScanOptions().setCaptureActivity(ScannerActivity::class.java)
    .setDesiredBarcodeFormats(ScanOptions.QR_CODE).setPrompt("Scan the QR from wt pair on your computer")
    .setBeepEnabled(false).setOrientationLocked(false).createScanIntent(context)
   ActivityScenario.launch<ScannerActivity>(intent).use { scenario ->
    scenario.onActivity { activity ->
     // Fixture-only evidence. Production scan windows keep FLAG_SECURE.
     activity.window.clearFlags(WindowManager.LayoutParams.FLAG_SECURE)
    }
    device.waitForIdle(3000)
    assertTrue("Camera prompt is visible", device.wait(Until.hasObject(By.text("Scan the QR from wt pair on your computer")), 15000))
    assertFalse("System ANR dialog obscures camera", device.hasObject(By.textContains("isn't responding")))
    captureEvidence(device, context, "camera-$theme")
   }
  }
  context.getSystemService(android.app.UiModeManager::class.java).setApplicationNightMode(android.app.UiModeManager.MODE_NIGHT_AUTO)
 }
}
