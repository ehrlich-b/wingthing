# Put Wingthing on your phone

1. Send the `wingthing.apk` file to your Android phone and open it from Files or
   your browser. If Android asks, open Settings and allow **Install unknown apps**
   or **Allow from this source** for that app. Return to the APK and tap **Install**.
   You can turn that permission off again afterward.
2. Open **Wingthing**. Tap **Pair a computer**, then **Open camera**. Allow camera
   and nearby-device access when asked.
3. On the Mac, open the terminal and run `wt pair`. If Wingthing is not running,
   start it with `wt wing start`, then run `wt pair`. Keep the phone and Mac on
   the same Wi-Fi for the first pairing.
4. Scan the QR. You will see the computer’s name and four emoji. Compare all four
   with the Mac’s terminal. If they match, tap **Emoji match · Pair** on the phone
   and press **Enter** on the Mac. If they differ, cancel on both screens.
5. The computer appears under **Your machines**. Tap it to see your sessions.
   **Needs you** means work is waiting for you; **Unread** means there is something
   new. Tap a session to open its terminal. Type and tap **Send ↵**. **Back to
   sessions** leaves the work running; **Stop** asks before ending it.

No account is needed on your phone. Pair another computer separately with its
own QR. If a QR expires, press Enter on the computer for a fresh one and scan again.
If scanning is difficult, use the pairing-link and six-word option; copy the link
and six words shown on your computer.

If the computer cannot be reached, check Wi-Fi. If you already use Tailscale,
connect it on both devices. For Wingthing remote access, Bryan sets up `wt login`
on the computer once and enables its relay access. The phone never logs in.

To forget a computer, open **Manage this pairing** and tap **Forget**. To revoke
access (especially if the phone is lost), on each paired computer run `wt devices`
and then `wt devices revoke <device-id>` using the ID shown in that list.

REQUIRED-UNRUN: a real phone scan. This APK has not yet been accepted on your
physical phone. Bryan should complete that first pairing with you, including
sending a message and stopping a disposable session.
