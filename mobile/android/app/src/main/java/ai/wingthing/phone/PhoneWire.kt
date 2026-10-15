package ai.wingthing.phone

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import bridge.Bridge
import bridge.Wire
import okhttp3.*
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.IOException
import java.net.Proxy
import java.util.UUID
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

// One wire per enrollment/connection; its selected carrier never changes after hello/start.
class PhoneWire(private val context: Context, private val metadata: JSONObject,
                private val discoverOverride: (() -> String?)? = null) : Wire, AutoCloseable {
    private val http = OkHttpClient.Builder().proxy(Proxy.NO_PROXY).cookieJar(CookieJar.NO_COOKIES)
        .followRedirects(false).followSslRedirects(false).retryOnConnectionFailure(false)
        .connectTimeout(4, TimeUnit.SECONDS).readTimeout(15, TimeUnit.SECONDS).callTimeout(20, TimeUnit.SECONDS).build()
    private var selected: String? = null
    private var relaySelected = false
    private var socket: WebSocket? = null
    private val replies = ArrayBlockingQueue<String>(2)
    var route: String = "Connecting…"
        private set
    override fun exchange(box: String, initial: Boolean): String {
        if (initial) { selected = null; relaySelected = false; closeSocket() }
        if (!initial) {
            if (relaySelected) return relay(box)
            return direct(selected ?: throw IOException("unreachable"), box)
        }
        val local = metadata.getString("local")
        val extra = metadata.optJSONArray("endpoints")
        val urls = (listOf(local) + (0 until (extra?.length() ?: 0)).map { extra!!.getString(it) }).distinct()
        require(urls.all { Bridge.localAllowed(it) } && Bridge.relayAllowed(metadata.optString("relay")))
        for (url in urls.filterNot { Bridge.isTailnet(it) }) {
            try { return choose(url, box) } catch (_: IOException) { }
        }
        val discovered = discoverOverride?.invoke() ?: if (discoverOverride == null) discover() else null
        if (discovered != null && discovered !in urls && Bridge.localAllowed(discovered)) {
            try { return choose(discovered, box) } catch (_: IOException) { }
        }
        for (url in urls.filter { Bridge.isTailnet(it) }) {
            try { return choose(url, box) } catch (_: IOException) { }
        }
        if (metadata.optString("relay").isEmpty()) throw IOException("unreachable")
        relaySelected = true; route = "Connected through relay · Encrypted"
        return relay(box)
    }
    private fun choose(url: String, box: String): String {
        val response = direct(url, box)
        selected = url
        route = if (Bridge.isTailnet(url)) "Connected through Tailscale · Encrypted" else "Connected on your network · Encrypted"
        return response
    }
    private fun direct(url: String, box: String): String {
        require(Bridge.localAllowed(url))
        val request = Request.Builder().url(url.trimEnd('/') + "/phone")
            .post(box.toRequestBody("application/json".toMediaType())).build()
        http.newCall(request).execute().use { response ->
            // A response that fails authentication must stop, never trigger fallback.
            if (!response.isSuccessful || response.request.url != request.url) throw IllegalStateException("bad_proof")
            val source = response.body?.source() ?: throw IllegalStateException("bad_proof")
            val buffer = okio.Buffer()
            while (!source.exhausted()) {
                val n = source.read(buffer, minOf(8192L, 266241L - buffer.size))
                if (buffer.size > 266240 || n < 0) throw IllegalStateException("too_large")
            }
            return buffer.readUtf8()
        }
    }
    private fun relay(box: String): String {
        val origin = metadata.optString("relay")
        require(origin.isNotEmpty() && Bridge.relayAllowed(origin))
        if (socket == null) {
            replies.clear()
            val opened = CountDownLatch(1)
            val url = origin.trimEnd('/').replaceFirst("https://", "wss://") + "/ws/phone?wing_id=" +
                java.net.URLEncoder.encode(metadata.getString("wing_id"), "UTF-8")
            socket = http.newWebSocket(Request.Builder().url(url).build(), object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) { opened.countDown() }
                override fun onMessage(webSocket: WebSocket, text: String) {
                    if (text.toByteArray().size > 266240 || !replies.offer(text)) { webSocket.cancel(); replies.clear(); replies.offer("failure") }
                }
                override fun onMessage(webSocket: WebSocket, bytes: okio.ByteString) { onMessage(webSocket, bytes.utf8()) }
                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) { replies.offer("failure"); opened.countDown() }
                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) { replies.offer("failure") }
            })
            if (!opened.await(15, TimeUnit.SECONDS)) { closeSocket(); throw IOException("unreachable") }
        }
        val id = UUID.randomUUID().toString()
        val envelope = JSONObject().put("type", "tunnel.req").put("purpose", "phone")
            .put("wing_id", metadata.getString("wing_id")).put("request_id", id).put("payload", box)
        if (socket?.send(envelope.toString()) != true) { closeSocket(); throw IOException("unreachable") }
        val text = replies.poll(15, TimeUnit.SECONDS) ?: run { closeSocket(); throw IOException("unreachable") }
        if (text == "failure") { closeSocket(); throw IOException("unreachable") }
        val reply = JSONObject(text)
        if (reply.optString("type") != "tunnel.res" || reply.optString("request_id") != id) {
            closeSocket(); throw IllegalStateException("bad_proof")
        }
        return reply.getString("payload")
    }
    @Suppress("DEPRECATION")
    private fun discover(): String? {
        val nsd = context.getSystemService(NsdManager::class.java)
        val done = CountDownLatch(1)
        var result: String? = null
        val name = Bridge.discoveryName(metadata.getString("wing_id"))
        val listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(type: String) { }
            override fun onServiceFound(info: NsdServiceInfo) {
                if (info.serviceName != name) return
                nsd.resolveService(info, object : NsdManager.ResolveListener {
                    override fun onResolveFailed(service: NsdServiceInfo, code: Int) { }
                    override fun onServiceResolved(service: NsdServiceInfo) {
                        val attributes = service.attributes
                        if (attributes["v"]?.toString(Charsets.UTF_8) != "1" ||
                            attributes["wing_id"]?.toString(Charsets.UTF_8) != metadata.getString("wing_id") ||
                            attributes["key"]?.toString(Charsets.UTF_8) != metadata.getString("key")) return
                        val url = "http://${service.host.hostAddress}:${service.port}"
                        if (Bridge.localAllowed(url) && !Bridge.isTailnet(url)) { result = url; done.countDown() }
                    }
                })
            }
            override fun onServiceLost(info: NsdServiceInfo) { }
            override fun onDiscoveryStopped(type: String) { done.countDown() }
            override fun onStartDiscoveryFailed(type: String, code: Int) { done.countDown() }
            override fun onStopDiscoveryFailed(type: String, code: Int) { done.countDown() }
        }
        try { nsd.discoverServices("_wingthing-phone._tcp.", NsdManager.PROTOCOL_DNS_SD, listener); done.await(3, TimeUnit.SECONDS) }
        catch (_: SecurityException) { }
        finally { try { nsd.stopServiceDiscovery(listener) } catch (_: Exception) { } }
        return result
    }
    private fun closeSocket() { socket?.cancel(); socket = null }
    override fun close() { closeSocket(); http.dispatcher.cancelAll(); http.connectionPool.evictAll() }
}
