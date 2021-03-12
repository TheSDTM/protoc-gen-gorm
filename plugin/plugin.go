package plugin

import (
	"fmt"
	"sort"
	"strings"

	"text/template"

	"github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/protoc-gen-gogo/generator"
	jgorm "github.com/jinzhu/gorm"
	"github.com/jinzhu/inflection"

	"log"

	gorm "github.com/TheSDTM/protoc-gen-gorm/options"

	pgs "github.com/lyft/protoc-gen-star"
	pgsgo "github.com/lyft/protoc-gen-star/lang/go"
)

const (
	typeMessage = 11
	typeEnum    = 14

	protoTypeTimestamp = "Timestamp" // last segment, first will be *google_protobufX
	protoTypeJSON      = "JSONValue"
	protoTypeUUID      = "UUID"
	protoTypeUUIDValue = "UUIDValue"
	protoTypeResource  = "Identifier"
	protoTypeInet      = "InetValue"
	protoTimeOnly      = "TimeOnly"
)

// DB Engine Enum
const (
	ENGINE_UNSET = iota
	ENGINE_POSTGRES
)

var wellKnownTypes = map[string]string{
	"StringValue": "*string",
	"DoubleValue": "*float64",
	"FloatValue":  "*float32",
	"Int32Value":  "*int32",
	"Int64Value":  "*int64",
	"UInt32Value": "*uint32",
	"UInt64Value": "*uint64",
	"BoolValue":   "*bool",
	//  "BytesValue" : "*[]byte",
}

var builtinTypes = map[string]struct{}{
	"bool": struct{}{},
	"int":  struct{}{},
	"int8": struct{}{}, "int16": struct{}{},
	"int32": struct{}{}, "int64": struct{}{},
	"uint":  struct{}{},
	"uint8": struct{}{}, "uint16": struct{}{},
	"uint32": struct{}{}, "uint64": struct{}{},
	"uintptr": struct{}{},
	"float32": struct{}{}, "float64": struct{}{},
	"string": struct{}{},
	"[]byte": struct{}{},
}

type OrmableType struct {
	OriginName string
	Name       string
	// Package     string
	File        pgs.File
	Fields      map[string]*Field
	FieldsOrder []string
}

type Field struct {
	ParentGoType string
	Type         string
	Package      string
	*gorm.GormFieldOptions
	ParentOriginName string
}

func NewOrmableType(oname string, file pgs.File) *OrmableType {
	return &OrmableType{
		OriginName:  oname,
		File:        file,
		Fields:      make(map[string]*Field),
		FieldsOrder: []string{},
	}
}

// OrmPlugin implements the plugin interface and creates GORM code from .protos
type OrmPlugin struct {
	// *generator.Generator
	*pgs.ModuleBase
	ctx pgsgo.Context
	tpl *template.Template

	wktPkgName        string
	dbEngine          int
	stringEnums       bool
	gateway           bool
	ormableTypes      map[string]*OrmableType
	EmptyFiles        []string
	currentPackage    string
	currentFile       pgs.File
	currentFileName   string
	currentFileBuffer []string
	fileImports       map[string]string
	messages          map[string]struct{}
	suppressWarn      bool
}

// Name identifies the plugin
func (p *OrmPlugin) Name() string {
	return "gorm"
}

// Init is called once after data structures are built but before
// code generation begins.
func (p *OrmPlugin) InitContext(c pgs.BuildContext) {
	p.ModuleBase.InitContext(c)
	p.ctx = pgsgo.InitContext(c.Parameters())

	p.fileImports = make(map[string]string)
	p.messages = make(map[string]struct{})
	p.ormableTypes = map[string]*OrmableType{}
	if strings.EqualFold(p.ctx.Params()["engine"], "postgres") {
		p.dbEngine = ENGINE_POSTGRES
	} else {
		p.dbEngine = ENGINE_UNSET
	}
	if strings.EqualFold(p.ctx.Params()["enums"], "string") {
		p.stringEnums = true
	}
	if _, ok := p.ctx.Params()["gateway"]; ok {
		p.gateway = true
	}
	if _, ok := p.ctx.Params()["quiet"]; ok {
		p.suppressWarn = true
	}
}

func (p *OrmPlugin) preparse(targets map[string]pgs.File) {
	for _, t := range targets {
		for _, msg := range t.Messages() {
			// We don't want to bother with the MapEntry stuff
			if msg.Descriptor().GetOptions().GetMapEntry() {
				continue
			}
			typeName := p.getMsgName(msg)
			p.messages[typeName] = struct{}{}

			if opts := getMessageOptions(msg); opts != nil && opts.GetOrmable() {
				ormable := NewOrmableType(typeName, t)
				if _, ok := p.ormableTypes[typeName]; !ok {
					p.ormableTypes[typeName] = ormable
				}
			}
		}
		for _, msg := range t.Messages() {
			typeName := p.getMsgName(msg)
			if p.isOrmable(typeName) {
				p.parseBasicFields(msg)
			}
		}
		for _, msg := range t.Messages() {
			typeName := p.getMsgName(msg)
			if p.isOrmable(typeName) {
				p.parseAssociations(msg)
				o := p.getOrmable(typeName)
				if p.hasPrimaryKey(o) {
					_, fd := p.findPrimaryKey(o)
					fd.ParentOriginName = o.OriginName
				}
			}
		}
	}
}

func (p *OrmPlugin) generate(f pgs.File) {
	if len(f.Messages()) == 0 {
		p.EmptyFiles = append(p.EmptyFiles, string(f.Name()))
		return
	}

	fileName := p.ctx.OutputPath(f).SetExt(".gorm.go")

	p.currentFile = f
	p.currentFileName = string(fileName)
	p.currentFileBuffer = []string{}

	for _, msg := range f.Messages() {
		typeName := p.getMsgName(msg)
		if p.isOrmable(typeName) {
			p.generateOrmable(msg)
			p.generateTableNameFunction(msg)
			p.generateConvertFunctions(msg)
			p.generateHookInterfaces(msg)
		}
	}

	if len(p.currentFileBuffer) == 0 {
		return
	}

	res := strings.Join(p.currentFileBuffer, "")
	tpl := template.New("headerTpl").Funcs(map[string]interface{}{
		"package": p.ctx.PackageName,
		"name":    p.ctx.Name,
		"generatedImports": func() string {
			importsRes := ""
			for k, v := range p.fileImports {
				importsRes += k + " \"" + v + "\"\n\t"
			}
			return importsRes
		},
		"generated_body": func() string {
			return res
		},
	})

	p.tpl = template.Must(tpl.Parse(headerTpl))

	p.AddGeneratorTemplateFile(p.currentFileName, p.tpl, f)
}

func (p *OrmPlugin) Execute(targets map[string]pgs.File, pkgs map[string]pgs.Package) []pgs.Artifact {
	p.preparse(targets)
	for _, t := range targets {
		p.generate(t)
	}
	return p.Artifacts()
}

func (p *OrmPlugin) parseBasicFields(msg pgs.Message) {
	typeName := p.getMsgName(msg)
	ormable := p.getOrmable(typeName)
	ormable.Name = fmt.Sprintf("%sORM", typeName)
	for _, field := range msg.Fields() {
		fieldOpts := getFieldOptions(field)
		if fieldOpts == nil {
			fieldOpts = &gorm.GormFieldOptions{}
		}
		if fieldOpts.GetDrop() {
			continue
		}
		tag := fieldOpts.GetTag()
		fieldName := generator.CamelCase(string(field.Name()))
		fieldType := string(p.ctx.Type(field))
		var typePackage string
		if p.dbEngine == ENGINE_POSTGRES && p.IsAbleToMakePQArray(fieldType) {
			p.fileImports["pqImport"] = "github.com/lib/pq"
			switch fieldType {
			case "[]bool":
				fieldType = fmt.Sprintf("%s.BoolArray", "pqImport")
				fieldOpts.Tag = tagWithType(tag, "bool[]")
			case "[]float64":
				fieldType = fmt.Sprintf("%s.Float64Array", "pqImport")
				fieldOpts.Tag = tagWithType(tag, "float[]")
			case "[]int64":
				fieldType = fmt.Sprintf("%s.Int64Array", "pqImport")
				fieldOpts.Tag = tagWithType(tag, "integer[]")
			case "[]string":
				fieldType = fmt.Sprintf("%s.StringArray", "pqImport")
				fieldOpts.Tag = tagWithType(tag, "text[]")
			default:
				continue
			}
		} else if (!field.Type().IsEmbed() || !p.isOrmable(fieldType)) && field.Type().IsRepeated() {
			// Not implemented yet
			continue

		} else if field.Type().IsEnum() {
			fieldType = "int32"
			if p.stringEnums {
				fieldType = "string"
			}
		} else if field.Type().IsEmbed() {
			//Check for WKTs or fields of nonormable types
			parts := strings.Split(fieldType, ".")
			rawType := parts[len(parts)-1]
			if v, exists := wellKnownTypes[rawType]; exists {
				// TODO perfilov
				// p.typesToRegister = append(p.typesToRegister, field.GetTypeName())
				p.wktPkgName = strings.Trim(parts[0], "*")
				fieldType = v
				typePackage = wktImport
				p.fileImports["wktImport"] = "github.com/golang/protobuf/ptypes/wrappers"
			} else if rawType == protoTypeUUID {
				fieldType = fmt.Sprintf("%s.UUID", "uuidImport")
				typePackage = uuidImport
				if p.dbEngine == ENGINE_POSTGRES {
					fieldOpts.Tag = tagWithType(tag, "uuid")
				}
			} else if rawType == protoTypeUUIDValue {
				fieldType = fmt.Sprintf("*%s.UUID", "uuidImport")
				typePackage = uuidImport
				if p.dbEngine == ENGINE_POSTGRES {
					fieldOpts.Tag = tagWithType(tag, "uuid")
				}
			} else if rawType == protoTypeTimestamp {
				p.fileImports["stdTimeImport"] = "time"
				typePackage = stdTimeImport
				fieldType = fmt.Sprintf("*%s.Time", "stdTimeImport")
			} else if rawType == protoTypeJSON {
				if p.dbEngine == ENGINE_POSTGRES {
					fieldType = fmt.Sprintf("*%s.Jsonb", "gormpqImport")
					typePackage = gormpqImport
					fieldOpts.Tag = tagWithType(tag, "jsonb")
				} else {
					// Potential TODO: add types we want to use in other/default DB engine
					continue
				}
			} else if rawType == protoTypeResource {
				tag := getFieldOptions(field).GetTag()
				ttype := tag.GetType()
				ttype = strings.ToLower(ttype)
				if strings.Contains(ttype, "char") {
					ttype = "char"
				}
				if strings.Contains(ttype, "array") || strings.ContainsAny(ttype, "[]") {
					ttype = "array"
				}
				switch ttype {
				case "uuid", "text", "char", "array", "cidr", "inet", "macaddr":
					fieldType = "*string"
				case "smallint", "integer", "bigint", "numeric", "smallserial", "serial", "bigserial":
					fieldType = "*int64"
				case "jsonb", "bytea":
					fieldType = "[]byte"
				case "":
					fieldType = "interface{}" // we do not know the type yet (if it association we will fix the type later)
				default:
					p.Fail("unknown tag type of atlas.rpc.Identifier")
				}
				if tag.GetNotNull() || tag.GetPrimaryKey() {
					fieldType = strings.TrimPrefix(fieldType, "*")
				}
			} else if rawType == protoTypeInet {
				fieldType = fmt.Sprintf("*%s.Inet", "gtypesImport")
				typePackage = gtypesImport
				if p.dbEngine == ENGINE_POSTGRES {
					fieldOpts.Tag = tagWithType(tag, "inet")
				} else {
					fieldOpts.Tag = tagWithType(tag, "varchar(48)")
				}
			} else if rawType == protoTimeOnly {
				fieldType = "string"
				fieldOpts.Tag = tagWithType(tag, "time")
			} else {
				continue
			}
		}
		f := &Field{Type: fieldType, Package: typePackage, GormFieldOptions: fieldOpts}
		if tname := getFieldOptions(field).GetReferenceOf(); tname != "" {
			if _, ok := p.messages[tname]; !ok {
				p.Fail("unknown message type in refers_to: ", tname, " in field: ", fieldName, " of type: ", typeName)
			}
			f.ParentOriginName = tname
		}
		ormable.Fields[fieldName] = f
		ormable.FieldsOrder = append(ormable.FieldsOrder, fieldName)
	}
	for _, field := range getMessageOptions(msg).GetInclude() {
		fieldName := generator.CamelCase(field.GetName())
		if _, ok := ormable.Fields[fieldName]; !ok {
			p.addIncludedField(ormable, field)
		} else {
			p.Fail("Cannot include", fieldName, "field into", ormable.Name, "as it aready exists there.")
		}
	}
}

func tagWithType(tag *gorm.GormTag, typename string) *gorm.GormTag {
	if tag == nil {
		tag = &gorm.GormTag{}
	}
	tag.Type = proto.String(typename)
	return tag
}

func (p *OrmPlugin) addIncludedField(ormable *OrmableType, field *gorm.ExtraField) {
	fieldName := generator.CamelCase(field.GetName())
	isPtr := strings.HasPrefix(field.GetType(), "*")
	rawType := strings.TrimPrefix(field.GetType(), "*")
	// cut off any package subpaths
	rawType = rawType[strings.LastIndex(rawType, ".")+1:]
	var typePackage string
	// Handle types with a package defined
	if field.GetPackage() != "" {
		alias := field.GetPackage()
		rawType = fmt.Sprintf("%s.%s", alias, rawType)
		typePackage = field.GetPackage()
	} else {
		// Handle types without a package defined
		if _, ok := builtinTypes[rawType]; ok {
			// basic type, 100% okay, no imports or changes needed
		} else if rawType == "Time" {
			typePackage = stdTimeImport
			p.fileImports["stdTimeImport"] = "time"
			rawType = fmt.Sprintf("%s.Time", "stdTimeImport")
		} else if rawType == "UUID" {
			// rawType = fmt.Sprintf("%s.UUID", "uuidImport")
			typePackage = uuidImport
			p.fileImports["uuidImport"] = "github.com/satori/go.uuid"
		} else if field.GetType() == "Jsonb" && p.dbEngine == ENGINE_POSTGRES {
			// rawType = fmt.Sprintf("%s.Jsonb", "gormpqImport")
			typePackage = gormpqImport
			p.fileImports["gormpqImport"] = "github.com/jinzhu/gorm/dialects/postgres"
		} else if rawType == "Inet" {
			// rawType = fmt.Sprintf("%s.Inet", "gtypesImport")
			typePackage = gtypesImport
			p.fileImports["gtypesImport"] = "github.com/TheSDTM/protoc-gen-gorm/types"
		} else {
			p.warning(`included field %q of type %q is not a recognized special type, and no package specified. This type is assumed to be in the same package as the generated code`,
				string(field.GetName()), field.GetType())
		}
	}
	if isPtr {
		rawType = fmt.Sprintf("*%s", rawType)
	}
	ormable.Fields[fieldName] = &Field{Type: rawType, Package: typePackage, GormFieldOptions: &gorm.GormFieldOptions{Tag: field.GetTag()}}
	ormable.FieldsOrder = append(ormable.FieldsOrder, fieldName)
}

func (p *OrmPlugin) isOrmable(typeName string) bool {
	parts := strings.Split(typeName, ".")
	_, ok := p.ormableTypes[strings.Trim(parts[len(parts)-1], "[]*")]
	return ok
}

func (p *OrmPlugin) getOrmable(typeName string) *OrmableType {
	parts := strings.Split(typeName, ".")
	if ormable, ok := p.ormableTypes[strings.TrimSuffix(strings.Trim(parts[len(parts)-1], "[]*"), "ORM")]; ok {
		return ormable
	} else {
		p.Fail(typeName, "is not ormable.")
		return nil
	}
}

func (p *OrmPlugin) getSortedFieldNames(fields map[string]*Field) []string {
	var keys []string
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (p *OrmPlugin) P(parts ...string) {
	p.currentFileBuffer = append(p.currentFileBuffer, parts...)
	p.currentFileBuffer = append(p.currentFileBuffer, "\n")
}

func (p *OrmPlugin) TypeName(msg pgs.Message) string {
	return string(msg.Name())
}

func (p *OrmPlugin) generateOrmable(message pgs.Message) {
	ormable := p.getOrmable(p.TypeName(message))
	p.P(`type `, ormable.Name, ` struct {`)
	for _, fieldName := range ormable.FieldsOrder {
		field := ormable.Fields[fieldName]
		p.P(fieldName, ` `, field.Type, p.renderGormTag(field))
	}
	p.P(`}`)
}

func (p *OrmPlugin) renderGormTag(field *Field) string {
	var gormRes string
	tag := field.GetTag()
	if tag == nil {
		tag = &gorm.GormTag{}
	}

	if tag.Column != nil {
		gormRes += fmt.Sprintf("column:%s;", tag.GetColumn())
	}
	if tag.Type != nil {
		gormRes += fmt.Sprintf("type:%s;", tag.GetType())
	}
	if tag.Size != nil {
		gormRes += fmt.Sprintf("size:%d;", tag.GetSize())
	}
	if tag.Precision != nil {
		gormRes += fmt.Sprintf("precision:%d;", tag.GetPrecision())
	}
	if tag.GetPrimaryKey() {
		gormRes += "primaryKey;"
	}
	if tag.GetUnique() {
		gormRes += "unique;"
	}
	if tag.Default != nil {
		gormRes += fmt.Sprintf("default:%s;", tag.GetDefault())
	}
	if tag.GetNotNull() {
		gormRes += "not null;"
	}
	if tag.GetAutoIncrement() {
		gormRes += "autoIncrement;"
	}
	if tag.Index != nil {
		if tag.GetIndex() == "" {
			gormRes += "index;"
		} else {
			gormRes += fmt.Sprintf("index:%s;", tag.GetIndex())
		}
	}
	if tag.UniqueIndex != nil {
		if tag.GetUniqueIndex() == "" {
			gormRes += "uniqueIndex;"
		} else {
			gormRes += fmt.Sprintf("uniqueIndex:%s;", tag.GetUniqueIndex())
		}
	}
	if tag.GetEmbedded() {
		gormRes += "embedded;"
	}
	if tag.EmbeddedPrefix != nil {
		gormRes += fmt.Sprintf("embeddedPrefix:%s;", tag.GetEmbeddedPrefix())
	}
	if tag.GetIgnore() {
		gormRes += "-;"
	}
	if tag.Check != nil {
		gormRes += fmt.Sprintf("check:%s;", tag.GetCheck())
	}
	if tag.CanRead != nil && *tag.CanRead == false {
		gormRes += "->:false;"
	}
	if tag.WritePermission != nil {
		switch *tag.WritePermission {
		case gorm.FieldWritePermission_FieldWritePermissionUpdateOnly:
			gormRes += "<-:update;"
		case gorm.FieldWritePermission_FieldWritePermissionCreateOnly:
			gormRes += "<-:create;"
		case gorm.FieldWritePermission_FieldWritePermissionNo:
			gormRes += "<-:false;"
		}
	}
	if tag.Constraint != nil {
		gormRes += "constraint:" + *tag.Constraint + ";"
	}

	var foreignKey, references, joinTable, joinForeignKey, joinReferences *string
	if hasOne := field.GetHasOne(); hasOne != nil {
		foreignKey = hasOne.ForeignKey
		references = hasOne.References
	} else if belongsTo := field.GetBelongsTo(); belongsTo != nil {
		foreignKey = belongsTo.ForeignKey
		references = belongsTo.References
	} else if hasMany := field.GetHasMany(); hasMany != nil {
		foreignKey = hasMany.ForeignKey
		references = hasMany.References
	} else if mtm := field.GetManyToMany(); mtm != nil {
		foreignKey = mtm.ForeignKey
		references = mtm.References
		joinTable = mtm.Jointable
		joinForeignKey = mtm.JoinForeignKey
		joinReferences = mtm.JoinReferences
	} else {
		foreignKey = tag.ForeignKey
		references = tag.References
		joinTable = tag.ManyToMany
		joinForeignKey = tag.JoinForeignKey
		joinReferences = tag.JoinReferences
	}

	if foreignKey != nil {
		gormRes += fmt.Sprintf("foreignKey:%s;", *foreignKey)
	}
	if references != nil {
		gormRes += fmt.Sprintf("references:%s;", *references)
	}
	if joinTable != nil {
		gormRes += fmt.Sprintf("many2many:%s;", *joinTable)
	}
	if joinForeignKey != nil {
		gormRes += fmt.Sprintf("joinForeignKey:%s;", *joinForeignKey)
	}
	if joinReferences != nil {
		gormRes += fmt.Sprintf("joinReferences:%s;", *joinReferences)
	}

	var gormTag string
	if gormRes != "" {
		gormTag = fmt.Sprintf("gorm:\"%s\"", strings.TrimRight(gormRes, ";"))
	}

	if gormTag == "" {
		return ""
	} else {
		return fmt.Sprintf("`%s`", gormTag)
	}
}

// generateTableNameFunction the function to set the gorm table name
// back to gorm default, removing "ORM" suffix
func (p *OrmPlugin) generateTableNameFunction(message pgs.Message) {
	typeName := p.TypeName(message)

	p.P(`// TableName overrides the default tablename generated by GORM`)
	p.P(`func (`, typeName, `ORM) TableName() string {`)

	tableName := inflection.Plural(jgorm.ToDBName(string(message.Name())))
	if opts := getMessageOptions(message); opts != nil && opts.Table != nil {
		tableName = opts.GetTable()
	}
	p.P(`return "`, tableName, `"`)
	p.P(`}`)
}

// generateMapFunctions creates the converter functions
func (p *OrmPlugin) generateConvertFunctions(message pgs.Message) {
	typeName := p.TypeName(message)
	ormable := p.getOrmable(generator.CamelCase(message.FullyQualifiedName()))

	///// To Orm
	p.P(`// ToORM runs the BeforeToORM hook if present, converts the fields of this`)
	p.P(`// object to ORM format, runs the AfterToORM hook, then returns the ORM object`)
	p.P(`func (m *`, typeName, `) ToORM (ctx context.Context) (`, typeName, `ORM, error) {`)
	p.P(`to := `, typeName, `ORM{}`)
	p.P(`var err error`)
	p.P(`if prehook, ok := interface{}(m).(`, typeName, `WithBeforeToORM); ok {`)
	p.P(`if err = prehook.BeforeToORM(ctx, &to); err != nil {`)
	p.P(`return to, err`)
	p.P(`}`)
	p.P(`}`)
	for _, field := range message.Fields() {
		// Checking if field is skipped
		if getFieldOptions(field).GetDrop() {
			continue
		}
		ofield := ormable.Fields[generator.CamelCase(string(field.Name()))]
		p.generateFieldConversion(message, field, true, ofield)
	}
	p.P(`if posthook, ok := interface{}(m).(`, typeName, `WithAfterToORM); ok {`)
	p.P(`err = posthook.AfterToORM(ctx, &to)`)
	p.P(`}`)
	p.P(`return to, err`)
	p.P(`}`)

	p.P()
	///// To Pb
	p.P(`// ToPB runs the BeforeToPB hook if present, converts the fields of this`)
	p.P(`// object to PB format, runs the AfterToPB hook, then returns the PB object`)
	p.P(`func (m *`, typeName, `ORM) ToPB (ctx context.Context) (`,
		typeName, `, error) {`)
	p.P(`to := `, typeName, `{}`)
	p.P(`var err error`)
	p.P(`if prehook, ok := interface{}(m).(`, typeName, `WithBeforeToPB); ok {`)
	p.P(`if err = prehook.BeforeToPB(ctx, &to); err != nil {`)
	p.P(`return to, err`)
	p.P(`}`)
	p.P(`}`)
	for _, field := range message.Fields() {
		// Checking if field is skipped
		if getFieldOptions(field).GetDrop() {
			continue
		}
		ofield := ormable.Fields[generator.CamelCase(string(field.Name()))]
		p.generateFieldConversion(message, field, false, ofield)
	}
	p.P(`if posthook, ok := interface{}(m).(`, typeName, `WithAfterToPB); ok {`)
	p.P(`err = posthook.AfterToPB(ctx, &to)`)
	p.P(`}`)
	p.P(`return to, err`)
	p.P(`}`)
}

// Output code that will convert a field to/from orm.
func (p *OrmPlugin) generateFieldConversion(message pgs.Message, field pgs.Field, toORM bool, ofield *Field) error {
	fieldName := generator.CamelCase(string(field.Name()))
	fieldType := string(p.ctx.Type(field))
	if field.Type().IsRepeated() { // Repeated Object ----------------------------------
		// Some repeated fields can be handled by github.com/lib/pq
		if p.dbEngine == ENGINE_POSTGRES && p.IsAbleToMakePQArray(fieldType) {
			p.P(`if m.Get`, fieldName, `() != nil {`)

			switch fieldType {
			case "[]bool":
				p.P(`to.`, fieldName, ` = make(pqImport.BoolArray, len(m.`, fieldName, `))`)
			case "[]float64":
				p.P(`to.`, fieldName, ` = make(pqImport.Float64Array, len(m.`, fieldName, `))`)
			case "[]int64":
				p.P(`to.`, fieldName, ` = make(pqImport.Int64Array, len(m.`, fieldName, `))`)
			case "[]string":
				p.P(`to.`, fieldName, ` = make(pqImport.StringArray, len(m.`, fieldName, `))`)
			}
			p.P(`copy(to.`, fieldName, `, m.`, fieldName, `)`)
			p.P(`}`)
		} else if p.isOrmable(fieldType) { // Repeated ORMable type
			//fieldType = strings.Trim(fieldType, "[]*")

			p.P(`for _, v := range m.`, fieldName, ` {`)
			p.P(`if v != nil {`)
			if toORM {
				p.P(`if temp`, fieldName, `, cErr := v.ToORM(ctx); cErr == nil {`)
			} else {
				p.P(`if temp`, fieldName, `, cErr := v.ToPB(ctx); cErr == nil {`)
			}
			p.P(`to.`, fieldName, ` = append(to.`, fieldName, `, &temp`, fieldName, `)`)
			p.P(`} else {`)
			p.P(`return to, cErr`)
			p.P(`}`)
			p.P(`} else {`)
			p.P(`to.`, fieldName, ` = append(to.`, fieldName, `, nil)`)
			p.P(`}`)
			p.P(`}`) // end repeated for
		} else {
			p.P(`// Repeated type `, fieldType, ` is not an ORMable message type`)
		}
	} else if field.Type().IsEnum() { // Singular Enum, which is an int32 ---
		if toORM {
			if p.stringEnums {
				p.P(`to.`, fieldName, ` = `, fieldType, `_name[int32(m.`, fieldName, `)]`)
			} else {
				p.P(`to.`, fieldName, ` = int32(m.`, fieldName, `)`)
			}
		} else {
			if p.stringEnums {
				p.P(`to.`, fieldName, ` = `, fieldType, `(`, fieldType, `_value[m.`, fieldName, `])`)
			} else {
				p.P(`to.`, fieldName, ` = `, fieldType, `(m.`, fieldName, `)`)
			}
		}
	} else if field.OneOf() != nil {
		if toORM {
			oneOf := field.OneOf()
			for i := range oneOf.Fields() {
				f := oneOf.Fields()[i]
				fieldName = generator.CamelCase(string(f.Name()))
				fieldType = string(p.ctx.Type(f))

				if fieldType == "string" {
					p.P(`if len(m.Get`, fieldName, `()) != 0 {`)
					p.P(`to.`, fieldName, ` = m.Get`, fieldName, `()`)
					p.P(`}`)
				} else {
					p.P(`if m.Get`, fieldName, `() != nil {`)
					p.P(`tmpConv, _ := m.Get`, fieldName, `().ToORM(ctx)`)
					p.P(`to.`, fieldName, ` = &tmpConv`)
					p.P(`}`)
				}
			}
		} else {
			oneOf := field.OneOf()
			oneOfFieldName := generator.CamelCase(string(oneOf.Name()))
			msgName := generator.CamelCase(string(oneOf.Message().Name()))
			for i := range oneOf.Fields() {
				f := oneOf.Fields()[i]
				fieldName = generator.CamelCase(string(f.Name()))
				fieldType = string(p.ctx.Type(f))

				if fieldType == "string" {
					p.P(`if len(m.`, fieldName, `) != 0 {`)
					p.P(`to.`, oneOfFieldName, ` = &`, msgName, `_`, fieldName, `{`, fieldName, `: m.`, fieldName, `}`)
					p.P(`}`)
				} else {
					p.P(`if m.`, fieldName, ` != nil {`)
					p.P(`tmpConv, _ := m.`, fieldName, `.ToPB(ctx)`)
					p.P(`to.`, oneOfFieldName, ` = &`, msgName, `_`, fieldName, `{`, fieldName, `: &tmpConv`, `}`)
					p.P(`}`)
				}
			}
		}
	} else if field.Type().IsEmbed() { // Singular Object -------------
		//Check for WKTs
		parts := strings.Split(fieldType, ".")
		coreType := parts[len(parts)-1]
		// Type is a WKT, convert to/from as ptr to base type
		if _, exists := wellKnownTypes[coreType]; exists { // Singular WKT -----
			if toORM {
				p.P(`if m.Get`, fieldName, `() != nil {`)
				p.P(`v := m.`, fieldName, `.Value`)
				p.P(`to.`, fieldName, ` = &v`)
				p.P(`}`)
			} else {
				p.P(`if m.Get`, fieldName, `() != nil {`)
				p.P(`to.`, fieldName, ` = &`, p.wktPkgName, ".", coreType,
					`{Value: *m.`, fieldName, `}`)
				p.P(`}`)
			}
		} else if coreType == protoTypeUUIDValue { // Singular UUIDValue type ----
			if toORM {
				p.P(`if m.Get`, fieldName, `() != nil {`)
				p.P(`tempUUID, uErr := `, "uuidImport", `.FromString(m.`, fieldName, `.Value)`)
				p.P(`if uErr != nil {`)
				p.P(`return to, uErr`)
				p.P(`}`)
				p.P(`to.`, fieldName, ` = &tempUUID`)
				p.P(`}`)
			} else {
				p.P(`if m.Get`, fieldName, `() != nil {`)
				p.P(`to.`, fieldName, ` = &`, "gtypesImport", `.UUIDValue{Value: m.`, fieldName, `.String()}`)
				p.P(`}`)
			}
		} else if coreType == protoTypeUUID { // Singular UUID type --------------
			if toORM {
				p.P(`if m.Get`, fieldName, `() != nil {`)
				p.P(`to.`, fieldName, `, err = `, "uuidImport", `.FromString(m.`, fieldName, `.Value)`)
				p.P(`if err != nil {`)
				p.P(`return to, err`)
				p.P(`}`)
				p.P(`} else {`)
				p.P(`to.`, fieldName, ` = `, "uuidImport", `.Nil`)
				p.P(`}`)
			} else {
				p.P(`to.`, fieldName, ` = &`, "gtypesImport", `.UUID{Value: m.`, fieldName, `.String()}`)
			}
		} else if coreType == protoTypeTimestamp { // Singular WKT Timestamp ---
			p.fileImports["ptypesImport"] = "github.com/golang/protobuf/ptypes"
			if toORM {
				p.P(`if m.Get`, fieldName, `() != nil {`)
				p.P(`var t time.Time`)
				p.P(`if t, err = `, "ptypesImport", `.Timestamp(m.`, fieldName, `); err != nil {`)
				p.P(`return to, err`)
				p.P(`}`)
				p.P(`to.`, fieldName, ` = &t`)
				p.P(`}`)
			} else {
				p.P(`if m.`, fieldName, ` != nil {`)
				p.P(`if to.`, fieldName, `, err = `, "ptypesImport", `.TimestampProto(*m.`, fieldName, `); err != nil {`)
				p.P(`return to, err`)
				p.P(`}`)
				p.P(`}`)
			}
		} else if coreType == protoTypeJSON {
			if p.dbEngine == ENGINE_POSTGRES {
				if toORM {
					p.P(`if m.Get`, fieldName, `() != nil {`)
					p.P(`to.`, fieldName, ` = &`, "gormpqImport", `.Jsonb{[]byte(m.`, fieldName, `.Value)}`)
					p.P(`}`)
				} else {
					p.P(`if m.Get`, fieldName, `() != nil {`)
					p.P(`to.`, fieldName, ` = &`, "gtypesImport", `.JSONValue{Value: string(m.`, fieldName, `.RawMessage)}`)
					p.P(`}`)
				}
			} // Potential TODO other DB engine handling if desired
		} else if coreType == protoTypeResource {
			resource := "nil" // assuming we do not know the PB type, nil means call codec for any resource
			if ofield != nil && ofield.ParentOriginName != "" {
				resource = "&" + ofield.ParentOriginName + "{}"
			}
			btype := strings.TrimPrefix(ofield.Type, "*")
			nillable := strings.HasPrefix(ofield.Type, "*")
			iface := ofield.Type == "interface{}"

			p.fileImports["resourceImport"] = "github.com/infobloxopen/atlas-app-toolkit/gorm/resource"

			if toORM {
				if nillable {
					p.P(`if m.Get`, fieldName, `() != nil {`)
				}
				switch btype {
				case "int64":
					p.P(`if v, err :=`, "resourceImport", `.DecodeInt64(`, resource, `, m.`, fieldName, `); err != nil {`)
					p.P(`	return to, err`)
					p.P(`} else {`)
					if nillable {
						p.P(`to.`, fieldName, ` = &v`)
					} else {
						p.P(`to.`, fieldName, ` = v`)
					}
					p.P(`}`)
				case "[]byte":
					p.P(`if v, err :=`, "resourceImport", `.DecodeBytes(`, resource, `, m.`, fieldName, `); err != nil {`)
					p.P(`	return to, err`)
					p.P(`} else {`)
					p.P(`	to.`, fieldName, ` = v`)
					p.P(`}`)
				default:
					p.P(`if v, err :=`, "resourceImport", `.Decode(`, resource, `, m.`, fieldName, `); err != nil {`)
					p.P(`return to, err`)
					p.P(`} else if v != nil {`)
					if nillable {
						p.P(`vv := v.(`, btype, `)`)
						p.P(`to.`, fieldName, ` = &vv`)
					} else if iface {
						p.P(`to.`, fieldName, `= v`)
					} else {
						p.P(`to.`, fieldName, ` = v.(`, btype, `)`)
					}
					p.P(`}`)
				}
				if nillable {
					p.P(`}`)
				}
			}

			if !toORM {
				if nillable {
					p.P(`if m.`, fieldName, `!= nil {`)
					p.P(`	if v, err := `, "resourceImport", `.Encode(`, resource, `, *m.`, fieldName, `); err != nil {`)
					p.P(`		return to, err`)
					p.P(`	} else {`)
					p.P(`		to.`, fieldName, ` = v`)
					p.P(`	}`)
					p.P(`}`)

				} else {
					p.P(`if v, err := `, "resourceImport", `.Encode(`, resource, `, m.`, fieldName, `); err != nil {`)
					p.P(`return to, err`)
					p.P(`} else {`)
					p.P(`to.`, fieldName, ` = v`)
					p.P(`}`)
				}
			}
		} else if coreType == protoTypeInet { // Inet type for Postgres only, currently
			if toORM {
				p.P(`if m.Get`, fieldName, `() != nil {`)
				p.P(`if to.`, fieldName, `, err = `, "gtypesImport", `.ParseInet(m.`, fieldName, `.Value); err != nil {`)
				p.P(`return to, err`)
				p.P(`}`)
				p.P(`}`)
			} else {
				p.P(`if m.`, fieldName, ` != nil && m.`, fieldName, `.IPNet != nil {`)
				p.P(`to.`, fieldName, ` = &`, "gtypesImport", `.InetValue{Value: m.`, fieldName, `.String()}`)
				p.P(`}`)
			}
		} else if coreType == protoTimeOnly { // Time only to support time via string
			if toORM {
				p.P(`if m.Get`, fieldName, `() != nil {`)
				p.P(`if to.`, fieldName, `, err = `, "gtypesImport", `.ParseTime(m.`, fieldName, `.Value); err != nil {`)
				p.P(`return to, err`)
				p.P(`}`)
				p.P(`}`)
			} else {
				p.P(`if m.`, fieldName, ` != "" {`)
				p.P(`if to.`, fieldName, `, err = `, "gtypesImport", `.TimeOnlyByString( m.`, fieldName, `); err != nil {`)
				p.P(`return to, err`)
				p.P(`}`)
				p.P(`}`)
			}
		} else if p.isOrmable(fieldType) {
			// Not a WKT, but a type we're building converters for
			if toORM {
				p.P(`if m.Get`, fieldName, `() != nil {`)
				p.P(`temp`, fieldName, `, err := m.Get`, fieldName, `().ToORM (ctx)`)
			} else {
				p.P(`if m.`, fieldName, ` != nil {`)
				p.P(`temp`, fieldName, `, err := m.`, fieldName, `.ToPB (ctx)`)
			}
			p.P(`if err != nil {`)
			p.P(`return to, err`)
			p.P(`}`)
			p.P(`to.`, fieldName, ` = &temp`, fieldName)
			p.P(`}`)
		}
	} else { // Singular raw ----------------------------------------------------
		p.P(`to.`, fieldName, ` = m.`, fieldName)
	}
	return nil
}

func (p *OrmPlugin) generateHookInterfaces(message pgs.Message) {
	typeName := p.TypeName(message)
	p.P(`// The following are interfaces you can implement for special behavior during ORM/PB conversions`)
	p.P(`// of type `, typeName, ` the arg will be the target, the caller the one being converted from`)
	p.P()
	for _, desc := range [][]string{
		{"BeforeToORM", typeName + "ORM", " called before default ToORM code"},
		{"AfterToORM", typeName + "ORM", " called after default ToORM code"},
		{"BeforeToPB", typeName, " called before default ToPB code"},
		{"AfterToPB", typeName, " called after default ToPB code"},
	} {
		p.P(`// `, typeName, `With`, desc[0], desc[2])
		p.P(`type `, typeName, `With`, desc[0], ` interface {`)
		p.P(desc[0], `(context.Context, *`, desc[1], `) error`)
		p.P(`}`)
		p.P()
	}
}

func (p *OrmPlugin) warning(format string, v ...interface{}) {
	if !p.suppressWarn {
		log.Printf("WARNING: "+format, v...)
	}
}
