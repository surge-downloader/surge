// Surge Download Manager - Background Service Worker
// Intercepts downloads and sends them to local Surge instance

const DEFAULT_PORT = 8080;
const MAX_PORT_SCAN = 100;
const INTERCEPT_ENABLED_KEY = "interceptEnabled";

// Cache the discovered port
let cachedPort = null;

// Find Surge by scanning ports
async function findSurgePort() {
    // Try cached port first
    if (cachedPort) {
        try {
            const response = await fetch(`http://127.0.0.1:${cachedPort}/health`, {
                method: "GET",
                signal: AbortSignal.timeout(500),
            });
            if (response.ok) return cachedPort;
        } catch { }
    }

    // Scan for available port
    for (let port = DEFAULT_PORT; port < DEFAULT_PORT + MAX_PORT_SCAN; port++) {
        try {
            const response = await fetch(`http://127.0.0.1:${port}/health`, {
                method: "GET",
                signal: AbortSignal.timeout(300),
            });
            if (response.ok) {
                cachedPort = port;
                console.log(`[Surge] Found server on port ${port}`);
                return port;
            }
        } catch { }
    }
    return null;
}

// Check if Surge is running
async function checkSurgeHealth() {
    const port = await findSurgePort();
    return port !== null;
}

// Send download request to Surge
async function sendToSurge(url, filename) {
    const port = await findSurgePort();
    if (!port) {
        console.error("[Surge] No server found");
        return false;
    }

    try {
        const response = await fetch(`http://127.0.0.1:${port}/download`, {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
            },
            body: JSON.stringify({
                url: url,
                filename: filename || "",
                path: "",
            }),
        });

        if (response.ok) {
            const data = await response.json();
            console.log("[Surge] Download queued:", data);
            return true;
        } else {
            console.error("[Surge] Failed to queue download:", response.status);
            return false;
        }
    } catch (error) {
        console.error("[Surge] Error sending to Surge:", error);
        return false;
    }
}

// Check if interception is enabled
async function isInterceptEnabled() {
    const result = await chrome.storage.local.get(INTERCEPT_ENABLED_KEY);
    // Default to enabled
    return result[INTERCEPT_ENABLED_KEY] !== false;
}



// Check if download is fresh (custom heuristic: < 60 seconds old AND in_progress)
function isFreshDownload(downloadItem) {
    // Filter out completed or interrupted downloads (history items)
    if (downloadItem.state && downloadItem.state !== "in_progress") {
        return false;
    }

    if (!downloadItem.startTime) return true; // unexpected, assume fresh

    const startTime = new Date(downloadItem.startTime).getTime();
    const now = Date.now();
    const diff = now - startTime;

    // If download started more than 60 seconds ago, it's likely history sync
    if (diff > 60000) {
        return false;
    }
    return true;
}

// Listen for downloads
chrome.downloads.onCreated.addListener(async (downloadItem) => {
    console.log("[Surge] Download detected:", downloadItem.url);

    // Check if interception is enabled
    const enabled = await isInterceptEnabled();
    if (!enabled) {
        console.log("[Surge] Interception disabled, using browser download");
        return;
    }

    // Filter historical downloads
    if (!isFreshDownload(downloadItem)) {
        console.log("[Surge] Ignoring historical download");
        return;
    }

    // Skip blob URLs and data URLs
    if (
        downloadItem.url.startsWith("blob:") ||
        downloadItem.url.startsWith("data:")
    ) {
        console.log("[Surge] Skipping blob/data URL");
        return;
    }

    // Check if Surge is running
    const surgeRunning = await checkSurgeHealth();
    if (!surgeRunning) {
        console.log("[Surge] Server not running, using browser download");
        return;
    }

    // Cancel browser download and send to Surge
    try {
        chrome.downloads.cancel(downloadItem.id);
        chrome.downloads.erase({ id: downloadItem.id });

        // Extract just the basename (Firefox compatibility)
        const filenameOnly = downloadItem.filename
            ? downloadItem.filename.split(/[/\\]/).pop()
            : "";

        const success = await sendToSurge(
            downloadItem.url,
            filenameOnly
        );

        if (success) {
            chrome.notifications.create({
                type: "basic",
                iconUrl: "icons/icon48.png",
                title: "Surge",
                message: `Download sent to Surge: ${filenameOnly || downloadItem.url.split("/").pop()}`,
            });
        }
    } catch (error) {
        console.error("[Surge] Failed to intercept download:", error);
    }
});

// Handle messages from popup
chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
    if (message.type === "checkHealth") {
        checkSurgeHealth().then((healthy) => {
            sendResponse({ healthy });
        });
        return true; // Keep channel open for async response
    }

    if (message.type === "getStatus") {
        isInterceptEnabled().then((enabled) => {
            sendResponse({ enabled });
        });
        return true;
    }

    if (message.type === "setStatus") {
        chrome.storage.local.set({ [INTERCEPT_ENABLED_KEY]: message.enabled });
        sendResponse({ success: true });
        return true;
    }
});

console.log("[Surge] Extension loaded");
