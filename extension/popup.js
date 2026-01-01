// Popup script for Surge extension

const statusDot = document.getElementById("statusDot");
const statusText = document.getElementById("statusText");
const interceptToggle = document.getElementById("interceptToggle");

// Check server health
async function checkHealth() {
    try {
        const response = await chrome.runtime.sendMessage({ type: "checkHealth" });
        if (response.healthy) {
            statusDot.className = "status-dot online";
            statusText.textContent = "Connected";
        } else {
            statusDot.className = "status-dot offline";
            statusText.textContent = "Offline";
        }
    } catch (error) {
        statusDot.className = "status-dot offline";
        statusText.textContent = "Offline";
    }
}

// Get current toggle status
async function getStatus() {
    try {
        const response = await chrome.runtime.sendMessage({ type: "getStatus" });
        interceptToggle.checked = response.enabled !== false;
    } catch (error) {
        interceptToggle.checked = true;
    }
}

// Handle toggle change
interceptToggle.addEventListener("change", async () => {
    await chrome.runtime.sendMessage({
        type: "setStatus",
        enabled: interceptToggle.checked,
    });
});

// Initialize
checkHealth();
getStatus();

// Refresh health status periodically
setInterval(checkHealth, 5000);
