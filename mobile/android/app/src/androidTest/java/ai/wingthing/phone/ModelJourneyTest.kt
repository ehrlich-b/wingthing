package ai.wingthing.phone

import android.app.Application
import android.os.SystemClock
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.rule.GrantPermissionRule
import bridge.Bridge
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.net.HttpURLConnection
import java.net.URL

@RunWith(AndroidJUnit4::class)
class ModelJourneyTest {
 @get:Rule val permissions: GrantPermissionRule = GrantPermissionRule.grant(*(if (android.os.Build.VERSION.SDK_INT >= 37) arrayOf("android.permission.ACCESS_LOCAL_NETWORK") else emptyArray()))
 private val instrumentation get() = InstrumentationRegistry.getInstrumentation()
 private fun control(op: String, id: String = "", name: String = ""): JSONObject {
  val c = URL("http://127.0.0.1:38473/fixture").openConnection() as HttpURLConnection
  c.requestMethod = "POST"; c.doOutput = true; c.connectTimeout = 5000; c.readTimeout = 5000
  c.outputStream.use { it.write(JSONObject().put("Op", op).put("ID", id).put("Name", name).toString().toByteArray()) }
  return c.inputStream.bufferedReader().use { JSONObject(it.readText()) }.also { c.disconnect() }
 }
 private fun waitState(model: PhoneModel, predicate: (PhoneState) -> Boolean): PhoneState {
  val deadline = SystemClock.elapsedRealtime() + 45000
  var state = PhoneState()
  while (SystemClock.elapsedRealtime() < deadline) {
   instrumentation.runOnMainSync { state = model.state }
   if (predicate(state)) return state
   // Recovery actions keep the current error screen visible until their async
   // storage/connection cleanup finishes. Await that work before judging it.
   if (state.screen == "error" && !state.busy) fail("Journey failed: ${state.error}")
   SystemClock.sleep(100)
  }
  fail("Timed out at ${state.screen}"); return state
 }
 @Test fun actualModelPairsAttachesStopsThenClearsContentOnLiveRevocation() {
  val link = control("create").getString("link")
  val id = JSONObject(Bridge.parseInvitation(link)).getString("id")
  lateinit var model: PhoneModel
  instrumentation.runOnMainSync { model = PhoneModel(instrumentation.targetContext.applicationContext as Application) }
  waitState(model) { !it.busy }
  try {
   instrumentation.runOnMainSync { model.pair(link) }
   waitState(model) { it.screen == "emoji" && !it.busy }
   instrumentation.runOnMainSync { model.confirm() }
   assertTrue(control("allow", id).getBoolean("ok"))
   val paired = waitState(model) { it.screen == "machines" && !it.busy && it.wings.any { wing -> wing.id == "fixture-main-wing" } }
   instrumentation.runOnMainSync { model.open(paired.wings.first { it.id == "fixture-main-wing" }) }
   val sessions = waitState(model) { it.screen == "sessions" && !it.busy }
   assertTrue(sessions.sessions.first().blocked); assertTrue(sessions.sessions.first().unread)
   instrumentation.runOnMainSync { model.attach(sessions.sessions.first()) }
   waitState(model) { it.screen == "terminal" && it.terminal.contains("fixture output") && !it.busy }
   instrumentation.runOnMainSync { model.input("hello\r") }
   waitState(model) { !it.busy }
   instrumentation.runOnMainSync { model.stopSession() }
   waitState(model) { it.ended && !it.busy }
   instrumentation.runOnMainSync { model.detach() }
   val resumed = waitState(model) { it.screen == "sessions" && !it.busy }
   instrumentation.runOnMainSync { model.attach(resumed.sessions.first()) }
   waitState(model) { it.screen == "terminal" && it.terminal.isNotEmpty() && !it.busy }
   assertTrue(control("revoke", name = "${android.os.Build.MODEL} phone").getBoolean("ok"))
   val revoked = waitState(model) { it.screen == "error" && it.error == "revoked" }
   assertEquals("", revoked.terminal)
  } finally {
   control("revoke", name = "${android.os.Build.MODEL} phone")
   instrumentation.runOnMainSync { model.forget() }
   waitState(model) { it.screen == "machines" && !it.busy }
  }
 }
}
