package store

// ChatSettingsStore is the optional part of a backend that keeps the settings of a chat with the
// chat itself: the broker asks for them when it loads the chat.
type ChatSettingsStore interface {
	ChatSettings(chatID string) (map[string]string, error)
}

// Releaser is the optional part of a backend that keeps a chat open while it is in use: the
// broker says when it lets the chat go.
type Releaser interface {
	Release(chatID string)
}
