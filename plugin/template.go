package plugin

const headerTpl = `package {{ package . }}

import (
	"context"
	"time"

	{{ generatedImports }}
)

{{ generated_body }}
`
