// Surge Download Manager - Background Service Worker
// Intercepts downloads and sends them to local Surge instance

const SURGE_URL = "http://127.0.0.1:8080";
const INTERCEPT_ENABLED_KEY = "interceptEnabled";

// Check if Surge is running
async function checkSurgeHealth() {
    try {
        const response = await fetch(`${SURGE_URL}/health`, {
            method: "GET",
            signal: AbortSignal.timeout(2000),
        });
        return response.ok;
    } catch (error) {
        console.log("[Surge] Server not reachable:", error.message);
        return false;
    }
}

// Send download request to Surge
async function sendToSurge(url, filename) {
    try {
        const response = await fetch(`${SURGE_URL}/download`, {
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

// Listen for downloads
chrome.downloads.onCreated.addListener(async (downloadItem) => {
    console.log("[Surge] Download detected:", downloadItem.url);

    // Check if interception is enabled
    const enabled = await isInterceptEnabled();
    if (!enabled) {
        console.log("[Surge] Interception disabled, using browser download");
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
        // Show notification to user
        chrome.notifications.create({
            type: "basic",
            iconUrl: "icons/icon48.png",
            title: "Surge Not Running",
            message: "Download proceeding in browser. Start Surge with: surge server",
        });
        return;
    }

    // Cancel browser download and send to Surge
    try {
        chrome.downloads.cancel(downloadItem.id);
        chrome.downloads.erase({ id: downloadItem.id });

        const success = await sendToSurge(
            downloadItem.url,
            downloadItem.filename || ""
        );

        if (success) {
            chrome.notifications.create({
                type: "basic",
                iconUrl: "icons/icon48.png",
                title: "Surge",
                message: `Download sent to Surge: ${downloadItem.filename || downloadItem.url.split("/").pop()}`,
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
