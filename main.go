package main

import (
	"github.com/TheSDTM/protoc-gen-gorm/plugin"

	pgs "github.com/lyft/protoc-gen-star"
)

func main() {
	plugin := &plugin.OrmPlugin{ModuleBase: &pgs.ModuleBase{}}
	pgs.Init(
		pgs.DebugEnv("DEBUG"),
	).RegisterModule(
		plugin,
	).RegisterPostProcessor(
	// pgsgo.GoFmt(),
	).Render()

	// response := command.GeneratePlugin(command.Read(), plugin, ".pb.gorm.go")
	// plugin.CleanFiles(response)
	// command.Write(response)
}
