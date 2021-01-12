package plugin

const headerTpl = `package {{ package . }}

import (
	"context"
	"time"
	
	
	uuidImport          "github.com/satori/go.uuid"
	gormpqImport        "github.com/jinzhu/gorm/dialects/postgres"
	gtypesImport        "github.com/TheSDTM/protoc-gen-gorm/types"
	ptypesImport        "github.com/golang/protobuf/ptypes"
	wktImport           "github.com/golang/protobuf/ptypes/wrappers"
	resourceImport      "github.com/infobloxopen/atlas-app-toolkit/gorm/resource"
	pqImport            "github.com/lib/pq"
	stdFmtImport        "fmt"
	stdCtxImport        "context"
	stdStringsImport    "strings"
	stdTimeImport       "time"
	encodingJsonImport  "encoding/json"
)

{{ generated_body }}
`
