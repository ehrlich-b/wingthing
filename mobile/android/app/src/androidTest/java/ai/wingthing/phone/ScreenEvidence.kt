package ai.wingthing.phone

import android.content.Context
import androidx.test.uiautomator.UiDevice
import org.junit.Assert.assertTrue
import org.junit.Assert.assertEquals
import java.io.File

// AGP uninstalls the fixture app after instrumentation, including external files.
// Preserve only fixture screenshots using the instrumentation shell's authority.
fun captureEvidence(device: UiDevice, context: Context, name: String) {
    require(name.matches(Regex("[a-z_-]+")))
    val directory = File(context.getExternalFilesDir(null), "screenshots").apply { mkdirs() }
    val file = File(directory, "$name.png")
    assertTrue(device.takeScreenshot(file))
    val destination = "/sdcard/Download/wingthing-evidence"
    val saved = "$destination/$name.png"
    // UiAutomation executes each command directly; it does not parse shell
    // operators or quoting. Keep these as separate commands with fixed paths.
    device.executeShellCommand("mkdir -p $destination")
    device.executeShellCommand("cp ${file.absolutePath} $saved")
    val export = device.executeShellCommand("ls $saved")
    assertEquals("Fixture screenshot was exported", saved, export.trim())
}
