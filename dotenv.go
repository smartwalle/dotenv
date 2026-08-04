// Package dotenv 提供 .env 文件的读取、查询和进程环境变量注入能力。
package dotenv

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// doubleQuotedReplacer 复用双引号值的转义替换规则，避免每次解析时创建替换器。
var doubleQuotedReplacer = strings.NewReplacer(
	`\n`, "\n",
	`\r`, "\r",
	`\t`, "\t",
	`\"`, `"`,
	`\\`, `\`,
)

// Env 保存从 .env 文件读取的值。文件中不存在的键会回退查询进程环境变量。
type Env struct {
	values map[string]string
}

// Load 读取指定的 .env 文件。未传入文件名时读取当前目录的 .env；多个文件按参数顺序
// 加载，后面的文件会覆盖前面文件中的同名键。不存在的文件视为空文件，使 Lookup 可以
// 回退查询进程环境变量。
func Load(filenames ...string) (*Env, error) {
	env := &Env{values: make(map[string]string)}
	if len(filenames) == 0 || len(filenames) == 1 && strings.TrimSpace(filenames[0]) == "" {
		filenames = []string{".env"}
	}
	for _, filename := range filenames {
		if err := loadFile(env, filename); err != nil {
			return nil, err
		}
	}
	return env, nil
}

func loadFile(env *Env, filename string) error {
	contents, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	// Windows 编辑器常会写入 UTF-8 BOM，但它不属于第一个变量名。
	lines := strings.Split(strings.TrimPrefix(string(contents), "\uFEFF"), "\n")
	for index := 0; index < len(lines); index++ {
		lineNumber := index + 1
		line := strings.TrimSuffix(lines[index], "\r")
		if quote, unclosed := unclosedOpeningQuote(line); unclosed {
			var multiline strings.Builder
			multiline.Grow(len(line))
			multiline.WriteString(line)
			for index+1 < len(lines) {
				index++
				nextLine := strings.TrimSuffix(lines[index], "\r")
				multiline.WriteByte('\n')
				multiline.WriteString(nextLine)
				if closingQuoteFrom(nextLine, quote) >= 0 {
					break
				}
			}
			line = multiline.String()
		}
		key, value, ok, err := parseLine(line)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", filename, lineNumber, err)
		}
		if ok {
			env.values[key] = value
		}
	}
	return nil
}

// Lookup 返回已加载文件中的键值；键不存在时回退查询进程环境变量。返回的布尔值表示
// 任一来源是否包含该键。
func (e *Env) Lookup(key string) (string, bool) {
	if value, ok := e.values[key]; ok {
		return value, true
	}
	return os.LookupEnv(key)
}

// Get 与 Lookup 类似，但在键不存在时返回空字符串。
func (e *Env) Get(key string) string {
	value, _ := e.Lookup(key)
	return value
}

// Inject 将从 .env 文件加载的值写入当前进程环境变量，并覆盖同名变量。仅通过 Lookup
// 回退获取的环境变量不会被注入。
func (e *Env) Inject() error {
	for key, value := range e.values {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set environment variable %q: %w", key, err)
		}
	}
	return nil
}

func validateEnvironmentVariable(key, value string) error {
	if key == "" || strings.ContainsAny(key, "=\x00") {
		return fmt.Errorf("invalid environment variable name %q", key)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("environment variable %q contains a NUL byte", key)
	}
	return nil
}

func parseLine(line string) (key, value string, ok bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
		line = strings.TrimSpace(line[len("export"):])
	}
	assignment, found := splitAssignment(line)
	if !found {
		return "", "", false, errors.New("expected KEY=VALUE")
	}
	key = strings.TrimSpace(assignment.key)
	if !validKey(key) {
		return "", "", false, errors.New("invalid key")
	}
	value = strings.TrimSpace(assignment.value)
	if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
		quote := value[0]
		end := closingQuote(value, quote)
		if end < 0 {
			return "", "", false, errors.New("unterminated quoted value")
		}
		trailing := strings.TrimSpace(value[end+1:])
		if trailing != "" && !strings.HasPrefix(trailing, "#") {
			return "", "", false, errors.New("unexpected content after quoted value")
		}
		value = value[1:end]
		if quote == '"' {
			value = doubleQuotedReplacer.Replace(value)
		}
	} else if index := unquotedComment(value); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	if err = validateEnvironmentVariable(key, value); err != nil {
		return "", "", false, err
	}
	return key, value, true, nil
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for index, char := range key {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9') || char == '.' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func unclosedOpeningQuote(line string) (byte, bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
		line = strings.TrimSpace(line[len("export"):])
	}
	assignment, found := splitAssignment(line)
	if !found {
		return 0, false
	}
	value := strings.TrimSpace(assignment.value)
	if len(value) == 0 || (value[0] != '\'' && value[0] != '"') {
		return 0, false
	}
	return value[0], closingQuote(value, value[0]) < 0
}

type assignmentParts struct {
	key   string
	value string
}

// splitAssignment 同时支持 KEY=value 和多个 dotenv 实现支持的 KEY: value 格式。
// 冒号后没有空白字符时，冒号会保留在键的候选内容中。
func splitAssignment(line string) (assignmentParts, bool) {
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case '=':
			return assignmentParts{key: line[:index], value: line[index+1:]}, true
		case ':':
			if index+1 == len(line) || line[index+1] == ' ' || line[index+1] == '\t' {
				return assignmentParts{key: line[:index], value: line[index+1:]}, true
			}
		}
	}
	return assignmentParts{}, false
}

func unquotedComment(value string) int {
	for index := 0; index < len(value); index++ {
		if value[index] == '#' && !escaped(value, index) {
			return index
		}
	}
	return -1
}

func closingQuote(value string, quote byte) int {
	index := closingQuoteFrom(value[1:], quote)
	if index < 0 {
		return -1
	}
	return index + 1
}

func closingQuoteFrom(value string, quote byte) int {
	for index := 0; index < len(value); index++ {
		if value[index] != quote {
			continue
		}
		if quote == '"' && escaped(value, index) {
			continue
		}
		return index
	}
	return -1
}

func escaped(value string, index int) bool {
	backslashes := 0
	for index--; index >= 0 && value[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 == 1
}
