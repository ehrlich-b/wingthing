package ai.wingthing.phone

import android.content.Context
import android.graphics.Canvas
import android.graphics.Typeface
import android.view.MotionEvent
import android.view.View
import com.termux.terminal.TerminalEmulator
import com.termux.terminal.TerminalOutput
import com.termux.terminal.TerminalSessionClient
import com.termux.view.TerminalRenderer
import java.lang.reflect.Proxy
import kotlin.math.max

// Termux's ANSI parser and renderer, connected to an existing remote session.
// Terminal-generated query replies and clipboard escape requests have no authority.
class RemoteTerminalView(context: Context) : View(context) {
    private val renderer = TerminalRenderer((14 * resources.displayMetrics.scaledDensity).toInt(), Typeface.MONOSPACE)
    private val client = Proxy.newProxyInstance(TerminalSessionClient::class.java.classLoader,
        arrayOf(TerminalSessionClient::class.java)) { _, method, _ -> if (method.name == "getTerminalCursorStyle") 0 else null } as TerminalSessionClient
    private val output = object : TerminalOutput() {
        override fun write(data: ByteArray, offset: Int, count: Int) { }
        override fun titleChanged(oldTitle: String?, newTitle: String?) { }
        override fun onCopyTextToClipboard(text: String?) { }
        override fun onPasteTextFromClipboard() { }
        override fun onBell() { }
        override fun onColorsChanged() { postInvalidate() }
    }
    val emulator = TerminalEmulator(output, 80, 24, 8, 16, 2000, client)
    private var content = ""
    private var revision = -1
    private var topRow = 0
    private var lastY = 0f
    init { setBackgroundColor(android.graphics.Color.rgb(16, 28, 25)); contentDescription = "Live session terminal. Swipe to scroll." }
    fun show(text: String, nextRevision: Int) {
        if (revision != nextRevision || !text.startsWith(content)) {
            emulator.reset(); append(text)
        } else if (text.length > content.length) append(text.substring(content.length))
        content = text; revision = nextRevision
        contentDescription = "Live session terminal. Swipe to scroll. " + emulator.screen.transcriptText.takeLast(4000)
        invalidate()
    }
    private fun append(text: String) { val bytes = text.toByteArray(Charsets.UTF_8); emulator.append(bytes, bytes.size) }
    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        emulator.resize(max(10, (w / renderer.fontWidth).toInt()), max(3, h / renderer.fontLineSpacing), renderer.fontWidth.toInt(), renderer.fontLineSpacing)
    }
    override fun onDraw(canvas: Canvas) { renderer.render(emulator, canvas, topRow, -1, -1, -1, -1) }
    override fun onTouchEvent(event: MotionEvent): Boolean {
        when (event.actionMasked) {
            MotionEvent.ACTION_DOWN -> { lastY = event.y; parent.requestDisallowInterceptTouchEvent(true) }
            MotionEvent.ACTION_MOVE -> {
                val lines = ((lastY - event.y) / renderer.fontLineSpacing).toInt()
                if (lines != 0) { topRow = (topRow + lines).coerceIn(-emulator.screen.activeTranscriptRows, 0); lastY = event.y; invalidate() }
            }
            MotionEvent.ACTION_UP -> { performClick(); parent.requestDisallowInterceptTouchEvent(false) }
        }
        return true
    }
    override fun performClick(): Boolean { super.performClick(); return true }
}
