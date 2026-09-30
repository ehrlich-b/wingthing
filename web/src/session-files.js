import { S, DOM } from './state.js';
import { sendTunnelRequest, sendTunnelStream } from './tunnel.js';
import { sha256 } from '@noble/hashes/sha2.js';

export const MAX_SESSION_UPLOAD_BYTES = 25 * 1024 * 1024;
export const MAX_SESSION_DOWNLOAD_BYTES = 100 * 1024 * 1024;
export const DEFAULT_SESSION_UPLOAD_CHUNK_BYTES = 128 * 1024;

function wingCapability(wing, name) {
    return !!wing && Array.isArray(wing.capabilities) && wing.capabilities.indexOf(name) !== -1;
}

function activeWing() {
    return S.wingsData.find(function(wing) { return wing.wing_id === S.ptyWingId; });
}

function wingFileLimit(wing, name, fallback) {
    var value = Number(wing && wing.file_limits && wing.file_limits[name]);
    return Number.isSafeInteger(value) && value > 0 ? value : fallback;
}

function formatMiB(bytes) {
    return Math.floor(bytes / (1024 * 1024)) + ' MiB';
}

function chunkBase64(bytes) {
    var binary = '';
    for (var i = 0; i < bytes.length; i += 0x8000) {
        binary += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
    }
    return btoa(binary);
}

function base64Bytes(value) {
    var binary = atob(value || '');
    var bytes = new Uint8Array(binary.length);
    for (var i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
    return bytes;
}

function bytesHex(bytes) {
    return Array.from(bytes).map(function(value) {
        return value.toString(16).padStart(2, '0');
    }).join('');
}

export function validSessionFilePath(path) {
    if (typeof path !== 'string' || !path.trim() || path.length > 4096) return false;
    var normalized = path.trim().replace(/\\/g, '/');
    if (normalized.startsWith('/') || normalized.indexOf('\0') !== -1) return false;
    return !normalized.split('/').some(function(part) { return part === '..'; });
}

export async function uploadSessionFile(send, wingId, sessionId, file, onProgress, maxBytes) {
    if (!file || !file.name) throw new Error('select a named file');
    maxBytes = maxBytes || MAX_SESSION_UPLOAD_BYTES;
    if (file.size > maxBytes) throw new Error('file exceeds the ' + formatMiB(maxBytes) + ' upload limit');

    var uploadId = '';
    var uploadHash = sha256.create();
    try {
        var begin = await send(wingId, {
            type: 'file.upload.begin', session_id: sessionId, name: file.name, size: file.size,
        });
        uploadId = begin.upload_id;
        if (!uploadId) throw new Error('wing did not return an upload ID');
        var chunkSize = Number(begin.chunk_size) || DEFAULT_SESSION_UPLOAD_CHUNK_BYTES;
        if (chunkSize < 1 || chunkSize > DEFAULT_SESSION_UPLOAD_CHUNK_BYTES) {
            throw new Error('wing returned an invalid upload chunk size');
        }
        var offset = 0;
        while (offset < file.size) {
            var end = Math.min(offset + chunkSize, file.size);
            var bytes = new Uint8Array(await file.slice(offset, end).arrayBuffer());
            uploadHash.update(bytes);
            var result = await send(wingId, {
                type: 'file.upload.chunk', upload_id: uploadId, offset: offset, data: chunkBase64(bytes),
            });
            if (Number(result.received) !== end) throw new Error('wing reported an unexpected upload offset');
            offset = end;
            if (onProgress) onProgress(offset, file.size);
        }
        var finished = await send(wingId, { type: 'file.upload.finish', upload_id: uploadId });
        if (!finished || Number(finished.size) !== file.size || !finished.sha256) {
            throw new Error('wing did not confirm the complete upload');
        }
        var uploadedSHA = bytesHex(uploadHash.digest());
        if (String(finished.sha256).toLowerCase() !== uploadedSHA) throw new Error('upload checksum mismatch');
        return finished;
    } catch (error) {
        if (uploadId) await send(wingId, { type: 'file.upload.cancel', upload_id: uploadId }).catch(function() {});
        throw error;
    }
}

export async function downloadSessionFile(stream, wingId, sessionId, path, onProgress, maxBytes) {
    if (!validSessionFilePath(path)) throw new Error('enter a path inside the session directory');
    maxBytes = maxBytes || MAX_SESSION_DOWNLOAD_BYTES;
    var metadata = null;
    var chunks = [];
    var received = 0;
    var reportedSHA = '';
    var downloadHash = sha256.create();
    await stream(wingId, { type: 'file.download', session_id: sessionId, path: path.trim() }, function(chunk) {
        if (!chunk) return;
        if (!metadata && chunk.name !== undefined && chunk.size !== undefined) {
            metadata = { name: String(chunk.name), size: Number(chunk.size), mime: String(chunk.mime || 'application/octet-stream') };
            if (!Number.isSafeInteger(metadata.size) || metadata.size < 0 || metadata.size > maxBytes) {
                throw new Error('file exceeds the ' + formatMiB(maxBytes) + ' download limit');
            }
            return;
        }
        if (chunk.data !== undefined) {
            if (!metadata) throw new Error('wing sent file data before metadata');
            var bytes = base64Bytes(chunk.data);
            received += bytes.length;
            if (received > metadata.size || received > maxBytes) throw new Error('wing sent too much file data');
            downloadHash.update(bytes);
            chunks.push(bytes);
            if (onProgress) onProgress(received, metadata.size);
        }
        if (chunk.sha256) reportedSHA = String(chunk.sha256).toLowerCase();
    });
    if (!metadata) throw new Error('wing did not return file metadata');
    if (received !== metadata.size) throw new Error('download ended before the complete file arrived');
    var bytes = new Uint8Array(received);
    var offset = 0;
    chunks.forEach(function(chunk) { bytes.set(chunk, offset); offset += chunk.length; });
    if (!reportedSHA) throw new Error('wing did not return a download checksum');
    var actualSHA = bytesHex(downloadHash.digest());
    if (actualSHA !== reportedSHA) throw new Error('download checksum mismatch');
    return { name: metadata.name, size: metadata.size, mime: metadata.mime, sha256: reportedSHA, bytes: bytes };
}

function startBrowserDownload(file) {
    var blob = new Blob([file.bytes], { type: file.mime });
    var url = URL.createObjectURL(blob);
    var anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = file.name;
    anchor.click();
    setTimeout(function() { URL.revokeObjectURL(url); }, 0);
}

function status(message, failed) {
    DOM.sessionFilesStatus.textContent = message || '';
    DOM.sessionFilesStatus.classList.toggle('failed', !!failed);
}

function refreshPanel() {
    var wing = activeWing();
    var canUpload = wingCapability(wing, 'session.file_upload.v1');
    var canDownload = wingCapability(wing, 'session.file_download.v1');
    var exportTargets = Array.isArray(wing && wing.exports) ? wing.exports.filter(function(target) {
        return target && target.type === 'folder' && target.name;
    }) : [];
    var canExport = wingCapability(wing, 'session.file_export.v1') && exportTargets.length > 0;
    DOM.sessionUploadBtn.disabled = !canUpload;
    DOM.sessionUploadNote.textContent = canUpload
        ? 'to the session directory · up to ' + formatMiB(wingFileLimit(wing, 'upload_bytes', MAX_SESSION_UPLOAD_BYTES))
        : 'upload is unavailable on this wing';
    DOM.sessionDownloadBtn.disabled = !canDownload;
    DOM.sessionExportSection.style.display = canExport ? '' : 'none';
    DOM.sessionExportBtn.disabled = !canExport;
    DOM.sessionExportTarget.innerHTML = canExport ? exportTargets.map(function(target) {
        return '<option value="' + String(target.name).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;') + '">' +
            String(target.name).replace(/&/g, '&amp;').replace(/</g, '&lt;') + '</option>';
    }).join('') : '';
}

export function refreshSessionFilesButton() {
    var wing = activeWing();
    var available = wingCapability(wing, 'session.file_upload.v1') ||
        wingCapability(wing, 'session.file_download.v1') || wingCapability(wing, 'session.file_export.v1');
    var active = !!S.ptySessionId && !S.spectating;
    DOM.sessionFilesBtn.style.display = active ? '' : 'none';
    DOM.sessionFilesBtn.disabled = active && !available;
    DOM.sessionFilesBtn.title = available ? 'Files for this session' : 'File transfer is unavailable on this wing';
    if (!active) hideSessionFiles();
    else if (DOM.sessionFilesPanel.style.display !== 'none') refreshPanel();
}

export function hideSessionFiles() {
    DOM.sessionFilesPanel.style.display = 'none';
    status('');
}

export function initSessionFiles() {
    DOM.sessionFilesBtn.addEventListener('click', function() {
        if (DOM.sessionFilesBtn.disabled) return;
        var opening = DOM.sessionFilesPanel.style.display === 'none';
        DOM.sessionFilesPanel.style.display = opening ? '' : 'none';
        if (opening) refreshPanel();
    });
    DOM.sessionFilesClose.addEventListener('click', hideSessionFiles);
    DOM.sessionUploadBtn.addEventListener('click', function() { if (!this.disabled) DOM.sessionUploadInput.click(); });
    DOM.sessionUploadInput.addEventListener('change', async function() {
        var files = Array.from(DOM.sessionUploadInput.files || []);
        DOM.sessionUploadInput.value = '';
        if (!files.length || !S.ptySessionId || !S.ptyWingId) return;
        var wingId = S.ptyWingId;
        var sessionId = S.ptySessionId;
        if (!wingCapability(activeWing(), 'session.file_upload.v1')) {
            status('upload is unavailable on this wing', true);
            return;
        }
        DOM.sessionUploadBtn.disabled = true;
        try {
            for (var i = 0; i < files.length; i++) {
                var file = files[i];
                status('uploading ' + file.name + ' (' + (i + 1) + '/' + files.length + ')');
                var result = await uploadSessionFile(sendTunnelRequest, wingId, sessionId, file, function(done, total) {
                    if (total) status('uploading ' + file.name + ' - ' + Math.floor(done * 100 / total) + '%');
                }, wingFileLimit(activeWing(), 'upload_bytes', MAX_SESSION_UPLOAD_BYTES));
                status('added ' + result.name + ' to the session directory');
            }
        } catch (error) {
            status('upload failed: ' + error.message, true);
        } finally {
            refreshPanel();
        }
    });
    DOM.sessionDownloadBtn.addEventListener('click', async function() {
        if (this.disabled || !S.ptySessionId || !S.ptyWingId) return;
        if (!wingCapability(activeWing(), 'session.file_download.v1')) {
            status('download is unavailable on this wing', true);
            return;
        }
        var path = DOM.sessionFilePath.value;
        this.disabled = true;
        status('downloading ' + path + '...');
        try {
            var file = await downloadSessionFile(sendTunnelStream, S.ptyWingId, S.ptySessionId, path, function(done, total) {
                if (total) status('downloading ' + Math.floor(done * 100 / total) + '%');
            }, wingFileLimit(activeWing(), 'download_bytes', MAX_SESSION_DOWNLOAD_BYTES));
            startBrowserDownload(file);
            status('downloaded ' + file.name);
        } catch (error) {
            status('download failed: ' + error.message, true);
        } finally {
            refreshPanel();
        }
    });
    DOM.sessionExportBtn.addEventListener('click', async function() {
        if (this.disabled || !S.ptySessionId || !S.ptyWingId) return;
        if (!wingCapability(activeWing(), 'session.file_export.v1')) {
            status('copying files is unavailable on this wing', true);
            return;
        }
        var path = DOM.sessionFilePath.value;
        if (!validSessionFilePath(path)) { status('enter a path inside the session directory', true); return; }
        this.disabled = true;
        status('copying ' + path + '...');
        try {
            var result = await sendTunnelRequest(S.ptyWingId, {
                type: 'file.export', session_id: S.ptySessionId, path: path.trim(), target: DOM.sessionExportTarget.value,
            });
            status('copied ' + result.name + ' to ' + result.target);
        } catch (error) {
            status('copy failed: ' + error.message, true);
        } finally {
            refreshPanel();
        }
    });
    document.addEventListener('keydown', function(event) {
        if (event.key === 'Escape' && DOM.sessionFilesPanel.style.display !== 'none') hideSessionFiles();
    });
}
