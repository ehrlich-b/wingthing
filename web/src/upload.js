export const MAX_SESSION_UPLOAD_BYTES = 25 * 1024 * 1024;
export const DEFAULT_SESSION_UPLOAD_CHUNK_BYTES = 128 * 1024;

function chunkBase64(bytes) {
    var binary = '';
    for (var i = 0; i < bytes.length; i += 0x8000) {
        binary += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
    }
    return btoa(binary);
}

export async function uploadSessionFile(send, wingId, sessionId, file, onProgress) {
    if (!file || !file.name) throw new Error('select a named file');
    if (file.size > MAX_SESSION_UPLOAD_BYTES) {
        throw new Error('file exceeds the 25 MiB upload limit');
    }

    var uploadId = '';
    try {
        var begin = await send(wingId, {
            type: 'file.upload.begin',
            session_id: sessionId,
            name: file.name,
            size: file.size,
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
            var result = await send(wingId, {
                type: 'file.upload.chunk',
                upload_id: uploadId,
                offset: offset,
                data: chunkBase64(bytes),
            });
            if (Number(result.received) !== end) throw new Error('wing reported an unexpected upload offset');
            offset = end;
            if (onProgress) onProgress(offset, file.size);
        }

        return await send(wingId, {
            type: 'file.upload.finish',
            upload_id: uploadId,
        });
    } catch (error) {
        if (uploadId) {
            await send(wingId, { type: 'file.upload.cancel', upload_id: uploadId }).catch(function() {});
        }
        throw error;
    }
}

export function showUploadToast(message, failed) {
    var existing = document.getElementById('session-upload-toast');
    if (existing) existing.remove();
    var toast = document.createElement('div');
    toast.id = 'session-upload-toast';
    toast.className = 'session-upload-toast' + (failed ? ' failed' : '');
    toast.textContent = message;
    document.body.appendChild(toast);
    setTimeout(function() {
        if (toast.isConnected) toast.remove();
    }, failed ? 8000 : 4000);
}
