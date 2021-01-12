package plugin

import (
	"fmt"
	"strings"

	gorm "github.com/TheSDTM/protoc-gen-gorm/options"
	"google.golang.org/protobuf/proto"

	pgs "github.com/lyft/protoc-gen-star"
)

func (p *OrmPlugin) getMsgName(o pgs.Message) string {
	fqTypeName := o.FullyQualifiedName()
	a := strings.Split(fqTypeName, ".")
	return a[len(a)-1]
}

// retrieves the GormMessageOptions from a message
func getMessageOptions(message pgs.Message) *gorm.GormMessageOptions {
	if message.Descriptor().Options == nil {
		return nil
	}
	res := proto.GetExtension(message.Descriptor().Options, gorm.E_Opts)
	if converted, ok := res.(*gorm.GormMessageOptions); ok {
		return converted
	}
	return nil
}

func getFieldOptions(field pgs.Field) *gorm.GormFieldOptions {
	if field.Descriptor().Options == nil {
		return nil
	}
	res := proto.GetExtension(field.Descriptor().Options, gorm.E_Field)
	if converted, ok := res.(*gorm.GormFieldOptions); ok {
		return converted
	}
	return nil
}

// func getServiceOptions(service *descriptor.ServiceDescriptorProto) *gorm.AutoServerOptions {
// 	if service.Options == nil {
// 		return nil
// 	}
// 	v, err := proto.GetExtension(service.Options, gorm.E_Server)
// 	if err != nil {
// 		return nil
// 	}
// 	opts, ok := v.(*gorm.AutoServerOptions)
// 	if !ok {
// 		return nil
// 	}
// 	return opts
// }

// func getMethodOptions(method *descriptor.MethodDescriptorProto) *gorm.MethodOptions {
// 	if method.Options == nil {
// 		return nil
// 	}
// 	v, err := proto.GetExtension(method.Options, gorm.E_Method)
// 	if err != nil {
// 		return nil
// 	}
// 	opts, ok := v.(*gorm.MethodOptions)
// 	if !ok {
// 		return nil
// 	}
// 	return opts
// }

func isSpecialType(typeName string) bool {
	parts := strings.Split(typeName, ".")
	if len(parts) > 2 { // what kinda format is this????
		panic(fmt.Sprintf(""))
	}
	if len(parts) == 1 { // native to this package = not special
		return false
	}
	// anything that looks like a google_protobufX should be considered special
	if strings.HasPrefix(strings.TrimLeft(typeName, "[]*"), "google_protobuf") {
		return true
	}
	switch parts[len(parts)-1] {
	case protoTypeJSON,
		protoTypeUUID,
		protoTypeUUIDValue,
		protoTypeResource,
		protoTypeInet,
		protoTimeOnly:
		return true
	}
	return false
}
