package ai.wingthing.phone
import android.os.Bundle
import android.os.Build
import android.view.View
import android.view.WindowInsets
import android.view.WindowManager
import com.journeyapps.barcodescanner.CaptureActivity
class ScannerActivity : CaptureActivity() {
 override fun onCreate(savedInstanceState: Bundle?) {
  super.onCreate(savedInstanceState)
  window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
  // Android 15+ enforces edge-to-edge for this target. Keep ZXing's prompt
  // above gesture navigation and away from display cutouts.
  if (Build.VERSION.SDK_INT >= 35) {
   findViewById<View>(android.R.id.content).apply {
    setOnApplyWindowInsetsListener { view, insets ->
     val bars = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.displayCutout())
     view.setPadding(bars.left, bars.top, bars.right, bars.bottom)
     insets
    }
    requestApplyInsets()
   }
  }
 }
}
