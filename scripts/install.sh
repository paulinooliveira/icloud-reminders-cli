#!/usr/bin/env bash
# Install script for icloud-reminders
# Installs a prebuilt native macOS EventKit binary.

set -e

REPO="tarekbecker/icloud-reminders-cli"
BINARY_NAME="reminders"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
    darwin) ;;
    *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

echo "Detected: $OS/$ARCH"

# Get latest release
LATEST_URL="https://github.com/${REPO}/releases/latest/download/icloud-reminders_${OS}_${ARCH}.tar.gz"

echo "Downloading from: $LATEST_URL"
curl -sL "$LATEST_URL" -o /tmp/icloud-reminders.tar.gz

echo "Extracting..."
tar -xzf /tmp/icloud-reminders.tar.gz -C /tmp

echo "Installing to $INSTALL_DIR..."
if [ -w "$INSTALL_DIR" ]; then
    mv "/tmp/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
else
    sudo mv "/tmp/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
fi
chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

echo "✅ Installed: $(${INSTALL_DIR}/${BINARY_NAME} --version 2>/dev/null || echo '${BINARY_NAME}')"
echo ""
echo "Next step: grant the binary Full Access in System Settings > Privacy & Security > Reminders"
