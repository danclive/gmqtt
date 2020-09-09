package mqtt

// 插件
type Plugin interface {
	// Load will be called in server.Run(). If return error, the server will panic.
	Load(service Server) error
	// Unload will be called when the server is shutdown, the return error is only for logging
	Unload() error
	// HookWrapper returns all hook wrappers that used by the plugin.
	// Return a empty wrapper  if the plugin does not need any hooks
	Hooks() Hooks
	// Name return the plugin name
	Name() string
}
