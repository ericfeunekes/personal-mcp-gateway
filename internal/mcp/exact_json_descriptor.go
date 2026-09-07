package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewExactJSONToolDescriptor preserves integer-valued provider data across SDK
// validation. SDK AddTool's typed adapter decodes through float64; this adapter
// validates with json.Number and retains the original JSON for typed decoding.
// Protocol dispatch, transport, annotations, and result metadata remain SDK-owned.
func NewExactJSONToolDescriptor[In, Out any](tool sdk.Tool, handler sdk.ToolHandlerFor[In, Out], args ArgumentSummarizer, result ResultSummarizer) (ToolDescriptor, error) {
	if err := validateToolName(tool.Name); err != nil {
		return ToolDescriptor{}, err
	}
	if handler == nil || args == nil || result == nil {
		return ToolDescriptor{}, errors.New("incomplete tool descriptor")
	}
	input, ok := tool.InputSchema.(*jsonschema.Schema)
	if !ok {
		return ToolDescriptor{}, errors.New("explicit input schema required")
	}
	output, ok := tool.OutputSchema.(*jsonschema.Schema)
	if !ok {
		return ToolDescriptor{}, errors.New("explicit output schema required")
	}
	inSchema, err := input.Resolve(nil)
	if err != nil {
		return ToolDescriptor{}, err
	}
	outSchema, err := output.Resolve(nil)
	if err != nil {
		return ToolDescriptor{}, err
	}
	copyTool := tool
	return ToolDescriptor{tool: copyTool, summarizeArgs: args, summarizeResult: result, register: func(server *sdk.Server) {
		server.AddTool(&copyTool, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			invalid := func(message string) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: message}}}, nil
			}
			if req.Params == nil || validateExactJSON(req.Params.Arguments, inSchema) != nil {
				return invalid("invalid tool arguments")
			}
			var in In
			if json.Unmarshal(req.Params.Arguments, &in) != nil {
				return invalid("invalid tool arguments")
			}
			res, out, err := handler(ctx, req, in)
			if err != nil {
				return nil, err
			}
			if res == nil {
				res = &sdk.CallToolResult{}
			}
			raw, err := json.Marshal(out)
			if err != nil {
				return nil, errors.New("unable to encode tool result")
			}
			if err = validateExactJSON(raw, outSchema); err != nil {
				return nil, errors.New("tool result does not match its schema")
			}
			res.StructuredContent = json.RawMessage(raw)
			if res.Content == nil {
				res.Content = []sdk.Content{&sdk.TextContent{Text: "Result available in structuredContent."}}
			}
			return res, nil
		})
	}}, nil
}

func validateExactJSON(raw []byte, schema *jsonschema.Resolved) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	value, err := schemaNumberValues(value)
	if err != nil {
		return err
	}
	return schema.Validate(value)
}

// jsonschema-go recognizes Go integer types for its type keyword, but treats
// json.Number as a string. Convert only the validation view; never reserialize it.
func schemaNumberValues(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		if len(v) > 128 {
			return nil, errors.New("numeric literal exceeds supported size")
		}
		if offset := strings.IndexAny(string(v), "eE"); offset >= 0 {
			exponent, err := strconv.ParseInt(string(v)[offset+1:], 10, 32)
			if err != nil || exponent > 308 || exponent < -324 {
				return nil, errors.New("numeric exponent exceeds supported range")
			}
		}
		if n, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return n, nil
		}
		if n, err := strconv.ParseUint(string(v), 10, 64); err == nil {
			return n, nil
		}
		rat, ok := new(big.Rat).SetString(string(v))
		if !ok {
			return nil, errors.New("invalid number")
		}
		if rat.IsInt() {
			if rat.Num().IsInt64() {
				return rat.Num().Int64(), nil
			}
			if rat.Num().IsUint64() {
				return rat.Num().Uint64(), nil
			}
			return nil, errors.New("integer exceeds supported range")
		}
		f, err := v.Float64()
		if err != nil || math.IsInf(f, 0) || math.Trunc(f) == f {
			return nil, errors.New("number cannot be validated without loss of fractional precision")
		}
		return f, nil
	case map[string]any:
		for key, child := range v {
			converted, err := schemaNumberValues(child)
			if err != nil {
				return nil, err
			}
			v[key] = converted
		}
	case []any:
		for i, child := range v {
			converted, err := schemaNumberValues(child)
			if err != nil {
				return nil, err
			}
			v[i] = converted
		}
	}
	return value, nil
}
