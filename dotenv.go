// Package dotenv 提供 .env 文件的读取、查询和进程环境变量注入能力。
package dotenv

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
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
	doubleQuotedReplacer := newDoubleQuotedReplacer()

	for _, filename := range filenames {
		if err := loadFile(env, doubleQuotedReplacer, filename); err != nil {
			return nil, err
		}
	}
	return env, nil
}

func loadFile(env *Env, doubleQuotedReplacer *strings.Replacer, filename string) error {
	file, err := os.Open(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()

	return parseReader(env, doubleQuotedReplacer, file, filename)
}

// Parse 读取 reader 中的 .env 内容并返回 Env。Parse 不会关闭 reader。
func Parse(reader io.Reader) (*Env, error) {
	if reader == nil {
		return nil, errors.New("dotenv: nil reader")
	}

	env := &Env{values: make(map[string]string)}
	if err := parseReader(env, newDoubleQuotedReplacer(), reader, ""); err != nil {
		return nil, err
	}
	return env, nil
}

// parseReader 逐行读取输入，将未闭合引号值合并为逻辑行后解析。source 用于定位解析错误；
// source 为空时，错误仅包含行号。
func parseReader(env *Env, doubleQuotedReplacer *strings.Replacer, input io.Reader, source string) error {
	reader := bufio.NewReader(input)
	lineNumber := 0
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if line == "" && errors.Is(readErr, io.EOF) {
			break
		}

		lineNumber++
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		// Windows 编辑器常会写入 UTF-8 BOM，但它不属于第一个变量名。
		if lineNumber == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		parseLineNumber := lineNumber
		if quote, unclosed := unclosedOpeningQuote(line); unclosed {
			var multiline strings.Builder
			multiline.Grow(len(line))
			multiline.WriteString(line)
			for {
				nextLine, nextReadErr := reader.ReadString('\n')
				if nextReadErr != nil && !errors.Is(nextReadErr, io.EOF) {
					return nextReadErr
				}
				if nextLine == "" && errors.Is(nextReadErr, io.EOF) {
					break
				}

				lineNumber++
				nextLine = strings.TrimSuffix(strings.TrimSuffix(nextLine, "\n"), "\r")
				multiline.WriteByte('\n')
				multiline.WriteString(nextLine)
				if closingQuoteFrom(nextLine, quote) >= 0 {
					break
				}
				if errors.Is(nextReadErr, io.EOF) {
					break
				}
			}
			line = multiline.String()
		}
		key, value, ok, err := parseLine(doubleQuotedReplacer, line)
		if err != nil {
			if source == "" {
				return fmt.Errorf("line %d: %w", parseLineNumber, err)
			}
			return fmt.Errorf("%s:%d: %w", source, parseLineNumber, err)
		}
		if ok {
			env.values[key] = value
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return nil
}

func newDoubleQuotedReplacer() *strings.Replacer {
	return strings.NewReplacer(
		`\n`, "\n",
		`\r`, "\r",
		`\t`, "\t",
		`\"`, `"`,
		`\\`, `\`,
	)
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

// LookupBool 返回键对应的布尔值。返回的布尔值表示键存在且值可解析为布尔值。
func (e *Env) LookupBool(key string) (bool, bool) {
	value, ok := e.Lookup(key)
	if !ok {
		return false, false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, false
	}
	return parsed, true
}

// Bool 返回键对应的布尔值。键不存在或值无法解析为布尔值时返回 false。
func (e *Env) Bool(key string) bool {
	value, _ := e.LookupBool(key)
	return value
}

// LookupInt 返回键对应的十进制整数。返回的布尔值表示键存在且值可解析为 int。
func (e *Env) LookupInt(key string) (int, bool) {
	value, ok := e.lookupInt(key, strconv.IntSize)
	return int(value), ok
}

// Int 返回键对应的十进制整数。键不存在、值无法解析或超出 int 范围时返回 0。
func (e *Env) Int(key string) int {
	value, _ := e.LookupInt(key)
	return value
}

// LookupInt8 返回键对应的十进制 8 位整数。返回的布尔值表示键存在且值可解析为 int8。
func (e *Env) LookupInt8(key string) (int8, bool) {
	value, ok := e.lookupInt(key, 8)
	return int8(value), ok
}

// Int8 返回键对应的十进制 8 位整数。键不存在、值无法解析或超出 int8 范围时返回 0。
func (e *Env) Int8(key string) int8 {
	value, _ := e.LookupInt8(key)
	return value
}

// LookupInt16 返回键对应的十进制 16 位整数。返回的布尔值表示键存在且值可解析为 int16。
func (e *Env) LookupInt16(key string) (int16, bool) {
	value, ok := e.lookupInt(key, 16)
	return int16(value), ok
}

// Int16 返回键对应的十进制 16 位整数。键不存在、值无法解析或超出 int16 范围时返回 0。
func (e *Env) Int16(key string) int16 {
	value, _ := e.LookupInt16(key)
	return value
}

// LookupInt32 返回键对应的十进制 32 位整数。返回的布尔值表示键存在且值可解析为 int32。
func (e *Env) LookupInt32(key string) (int32, bool) {
	value, ok := e.lookupInt(key, 32)
	return int32(value), ok
}

// Int32 返回键对应的十进制 32 位整数。键不存在、值无法解析或超出 int32 范围时返回 0。
func (e *Env) Int32(key string) int32 {
	value, _ := e.LookupInt32(key)
	return value
}

// LookupInt64 返回键对应的十进制 64 位整数。返回的布尔值表示键存在且值可解析为 int64。
func (e *Env) LookupInt64(key string) (int64, bool) {
	return e.lookupInt(key, 64)
}

// Int64 返回键对应的十进制 64 位整数。键不存在、值无法解析或超出 int64 范围时返回 0。
func (e *Env) Int64(key string) int64 {
	value, _ := e.LookupInt64(key)
	return value
}

// LookupUint 返回键对应的十进制无符号整数。返回的布尔值表示键存在且值可解析为 uint。
func (e *Env) LookupUint(key string) (uint, bool) {
	value, ok := e.lookupUint(key, strconv.IntSize)
	return uint(value), ok
}

// Uint 返回键对应的十进制无符号整数。键不存在、值无法解析、为负数或超出 uint 范围时返回 0。
func (e *Env) Uint(key string) uint {
	value, _ := e.LookupUint(key)
	return value
}

// LookupUint8 返回键对应的十进制 8 位无符号整数。返回的布尔值表示键存在且值可解析为 uint8。
func (e *Env) LookupUint8(key string) (uint8, bool) {
	value, ok := e.lookupUint(key, 8)
	return uint8(value), ok
}

// Uint8 返回键对应的十进制 8 位无符号整数。键不存在、值无法解析、为负数或超出 uint8 范围时返回 0。
func (e *Env) Uint8(key string) uint8 {
	value, _ := e.LookupUint8(key)
	return value
}

// LookupUint16 返回键对应的十进制 16 位无符号整数。返回的布尔值表示键存在且值可解析为 uint16。
func (e *Env) LookupUint16(key string) (uint16, bool) {
	value, ok := e.lookupUint(key, 16)
	return uint16(value), ok
}

// Uint16 返回键对应的十进制 16 位无符号整数。键不存在、值无法解析、为负数或超出 uint16 范围时返回 0。
func (e *Env) Uint16(key string) uint16 {
	value, _ := e.LookupUint16(key)
	return value
}

// LookupUint32 返回键对应的十进制 32 位无符号整数。返回的布尔值表示键存在且值可解析为 uint32。
func (e *Env) LookupUint32(key string) (uint32, bool) {
	value, ok := e.lookupUint(key, 32)
	return uint32(value), ok
}

// Uint32 返回键对应的十进制 32 位无符号整数。键不存在、值无法解析、为负数或超出 uint32 范围时返回 0。
func (e *Env) Uint32(key string) uint32 {
	value, _ := e.LookupUint32(key)
	return value
}

// LookupUint64 返回键对应的十进制 64 位无符号整数。返回的布尔值表示键存在且值可解析为 uint64。
func (e *Env) LookupUint64(key string) (uint64, bool) {
	return e.lookupUint(key, 64)
}

// Uint64 返回键对应的十进制 64 位无符号整数。键不存在、值无法解析或为负数时返回 0。
func (e *Env) Uint64(key string) uint64 {
	value, _ := e.LookupUint64(key)
	return value
}

// LookupFloat32 返回键对应的 32 位浮点数。返回的布尔值表示键存在且值可解析为 float32。
func (e *Env) LookupFloat32(key string) (float32, bool) {
	value, ok := e.lookupFloat(key, 32)
	return float32(value), ok
}

// Float32 返回键对应的 32 位浮点数。键不存在、值无法解析或超出 float32 范围时返回 0。
func (e *Env) Float32(key string) float32 {
	value, _ := e.LookupFloat32(key)
	return value
}

// LookupFloat64 返回键对应的 64 位浮点数。返回的布尔值表示键存在且值可解析为 float64。
func (e *Env) LookupFloat64(key string) (float64, bool) {
	return e.lookupFloat(key, 64)
}

// Float64 返回键对应的 64 位浮点数。键不存在或值无法解析为浮点数时返回 0。
func (e *Env) Float64(key string) float64 {
	value, _ := e.LookupFloat64(key)
	return value
}

// LookupDuration 返回键对应的时长。返回的布尔值表示键存在且值可按 time.ParseDuration 解析。
func (e *Env) LookupDuration(key string) (time.Duration, bool) {
	value, ok := e.Lookup(key)
	if !ok {
		return 0, false
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// Duration 返回键对应的时长。键不存在或值无法按 time.ParseDuration 解析时返回 0。
func (e *Env) Duration(key string) time.Duration {
	value, _ := e.LookupDuration(key)
	return value
}

func (e *Env) lookupInt(key string, bitSize int) (int64, bool) {
	value, ok := e.Lookup(key)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, bitSize)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func (e *Env) lookupUint(key string, bitSize int) (uint64, bool) {
	value, ok := e.Lookup(key)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseUint(value, 10, bitSize)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func (e *Env) lookupFloat(key string, bitSize int) (float64, bool) {
	value, ok := e.Lookup(key)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, bitSize)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// Inject 将从 .env 文件加载的值写入当前进程环境变量，并覆盖同名变量。
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

func parseLine(doubleQuotedReplacer *strings.Replacer, line string) (key, value string, ok bool, err error) {
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
