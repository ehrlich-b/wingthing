package ai.wingthing.phone
import org.junit.Assert.*
import org.junit.Test
class FailureCopyTest {
 @Test fun identityMismatchRequiresFreshApproval() {
  val copy = failureCopy("identity_mismatch", "Mac")
  assertEquals("Identity mismatch", copy.first)
  assertTrue(copy.second.contains("Pairing stopped"))
  assertTrue(copy.second.contains("Check the computer"))
 }
 @Test fun networkFailureExplainsFreeDirectOptions() {
  val copy = failureCopy("unreachable", "Mac").second
  assertTrue(copy.contains("same Wi-Fi"))
  assertTrue(copy.contains("Tailscale"))
  assertTrue(copy.contains("wt login"))
 }
 @Test fun expiredAndUsedShareRecovery() {
  assertEquals(failureCopy("expired", "Mac"), failureCopy("used", "Mac"))
  assertTrue(failureCopy("expired", "Mac").second.contains("press Enter"))
 }
}
