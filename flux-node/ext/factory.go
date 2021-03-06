package ext

import (
	"github.com/bytepowered/flux/flux-node"
	"github.com/bytepowered/flux/flux-pkg"
)

var (
	typedFactories = make(map[string]flux.Factory, 16)
)

func RegisterFactory(typeName string, factory flux.Factory) {
	typeName = fluxpkg.MustNotEmpty(typeName, "typeName is empty")
	typedFactories[typeName] = fluxpkg.MustNotNil(factory, "Factory is nil").(flux.Factory)
}

func FactoryByType(typeName string) (flux.Factory, bool) {
	typeName = fluxpkg.MustNotEmpty(typeName, "typeName is empty")
	f, o := typedFactories[typeName]
	return f, o
}
