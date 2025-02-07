package codegen

import (
	"fmt"
	"strconv"
	"strings"

	"goa.design/goa/v3/expr"

	"goa.design/goa/v3/codegen"
)

type (
	// protoBufScope is the scope for protocol buffer attribute types.
	protoBufScope struct {
		scope *codegen.NameScope
	}
)

// Name returns the protocol buffer type name.
func (p *protoBufScope) Name(att *expr.AttributeExpr, pkg string) string {
	return protoBufGoFullTypeName(att, pkg, p.scope)
}

// Ref returns the protocol buffer type reference.
func (p *protoBufScope) Ref(att *expr.AttributeExpr, pkg string) string {
	return protoBufGoFullTypeRef(att, pkg, p.scope)
}

// Field returns the field name as generated by protocol buffer compiler.
// NOTE: protoc does not care about common initialisms like api -> API so we
// first transform the name into snake case to end up with Api.
func (p *protoBufScope) Field(att *expr.AttributeExpr, name string, firstUpper bool) string {
	return protoBufifyAtt(att, codegen.SnakeCase(name), firstUpper)
}

// Scope returns the name scope.
func (p *protoBufScope) Scope() *codegen.NameScope {
	return p.scope
}

// protoBufTypeContext returns a contextual attribute for the protocol buffer type.
func protoBufTypeContext(pkg string, scope *codegen.NameScope) *codegen.AttributeContext {
	ctx := codegen.NewAttributeContext(false, true, true, pkg, scope)
	ctx.Scope = &protoBufScope{scope: scope}
	return ctx
}

// makeProtoBufMessage recursively transforms the given attribute expression
// to generate a valid protocol buffer message definition in the proto file.
// A protocol buffer message is always a user type in goa v2.
//
// NOTE: Protocol buffer does not provide native support for nested
// arrays/maps (See grpc/docs/FAQ.md)
//
// makeProtoBufMessage ensures the resulting attribute is a user type. If the
// given attribute type is a primitive, array, or a map, it wraps the given
// attribute under an attribute with name "field" and RPC tag number 1. For,
// nested arrays/maps, the inner array/map is wrapped into a user type.
func makeProtoBufMessage(att *expr.AttributeExpr, tname string, scope *codegen.NameScope) *expr.AttributeExpr {
	att = expr.DupAtt(att)
	switch dt := att.Type.(type) {
	case expr.Primitive:
		wrapAttr(att, tname)
		return att
	case expr.UserType:
		if dt == expr.Empty {
			// Empty type must generate a message definition
			att.Type = &expr.UserTypeExpr{
				TypeName:      tname,
				AttributeExpr: &expr.AttributeExpr{Type: &expr.Object{}},
			}
			return att
		} else if rt, ok := dt.(*expr.ResultTypeExpr); ok && expr.IsArray(rt) {
			// result type collection
			wrapAttr(att, tname)
		}
	case *expr.Array, *expr.Map:
		wrapAttr(att, tname)
	case *expr.Object:
		att.Type = &expr.UserTypeExpr{
			TypeName:      tname,
			AttributeExpr: expr.DupAtt(att),
		}
	}
	// wrap nested arrays/maps
	n := ""
	makeProtoBufMessageR(att, &n, scope)
	return att
}

// makeProtoBufMessageR is the recursive implementation of makeProtoBufMessage.
func makeProtoBufMessageR(att *expr.AttributeExpr, tname *string, scope *codegen.NameScope, seen ...map[string]struct{}) {
	wrap := func(att *expr.AttributeExpr, tname string) {
		switch dt := att.Type.(type) {
		case *expr.Array:
			wrapAttr(att, "ArrayOf"+tname+
				protoBufify(protoBufMessageDef(dt.ElemType, scope), true))
		case *expr.Map:
			wrapAttr(att, tname+"MapOf"+
				protoBufify(protoBufMessageDef(dt.KeyType, scope), true)+
				protoBufify(protoBufMessageDef(dt.ElemType, scope), true))
		}
	}
	switch dt := att.Type.(type) {
	case expr.UserType:
		var s map[string]struct{}
		if len(seen) > 0 {
			s = seen[0]
		} else {
			s = make(map[string]struct{})
			seen = append(seen, s)
		}
		if _, ok := s[dt.ID()]; ok {
			return
		}
		s[dt.ID()] = struct{}{}
		if rt, ok := dt.(*expr.ResultTypeExpr); ok && expr.IsArray(rt) {
			wrapAttr(rt.Attribute(), rt.Name())
		}
		makeProtoBufMessageR(dt.Attribute(), tname, scope, seen...)
	case *expr.Array:
		makeProtoBufMessageR(dt.ElemType, tname, scope, seen...)
		wrap(dt.ElemType, *tname)
	case *expr.Map:
		// need not worry about map keys because protocol buffer supports
		// only primitives as map keys.
		makeProtoBufMessageR(dt.ElemType, tname, scope, seen...)
		wrap(dt.ElemType, *tname)
	case *expr.Object:
		for _, nat := range *dt {
			makeProtoBufMessageR(nat.Attribute, tname, scope, seen...)
		}
	}
}

// wrapAttr makes the attribute type a user type by wrapping the given
// attribute into an attribute named "field".
func wrapAttr(att *expr.AttributeExpr, tname string) {
	wrap := func(att *expr.AttributeExpr) *expr.AttributeExpr {
		return &expr.AttributeExpr{
			Type: &expr.Object{
				&expr.NamedAttributeExpr{
					Name: "field",
					Attribute: &expr.AttributeExpr{
						Type: att.Type,
						Meta: expr.MetaExpr{"rpc:tag": []string{"1"}},
					},
				},
			},
		}
	}
	switch dt := att.Type.(type) {
	case expr.UserType:
		// Don't change the original user type. Create a copy and wrap that.
		ut := expr.Dup(dt).(expr.UserType)
		ut.SetAttribute(wrap(ut.Attribute()))
		att.Type = ut
	default:
		att.Type = &expr.UserTypeExpr{
			TypeName:      tname,
			AttributeExpr: wrap(att),
		}
	}
}

// unwrapAttr returns the attribute under the attribute name "field".
// If "field" does not exist, it returns the given attribute.
func unwrapAttr(att *expr.AttributeExpr) *expr.AttributeExpr {
	if a := att.Find("field"); a != nil {
		return a
	}
	return att
}

// protoBufMessageName returns the protocol buffer message name of the given
// attribute type.
func protoBufMessageName(att *expr.AttributeExpr, s *codegen.NameScope) string {
	return protoBufFullMessageName(att, "", s)
}

// protoBufFullMessageName returns the protocol buffer message name of the
// given user type qualified with the given package name if applicable.
func protoBufFullMessageName(att *expr.AttributeExpr, pkg string, s *codegen.NameScope) string {
	switch actual := att.Type.(type) {
	case expr.UserType:
		n := s.HashedUnique(actual, protoBufify(actual.Name(), true), "")
		if pkg == "" {
			return n
		}
		return pkg + "." + n
	case expr.CompositeExpr:
		return protoBufFullMessageName(actual.Attribute(), pkg, s)
	default:
		panic(fmt.Sprintf("data type is not a user type: received type %T", actual)) // bug
	}
}

// protoBufGoFullTypeName returns the protocol buffer type name for the given
// attribute generated after compiling the proto file (in *.pb.go).
func protoBufGoTypeName(att *expr.AttributeExpr, s *codegen.NameScope) string {
	return protoBufGoFullTypeName(att, "", s)
}

// protoBufGoFullTypeName returns the protocol buffer type name qualified with
// the given package name for the given attribute generated after compiling
// the proto file (in *.pb.go).
func protoBufGoFullTypeName(att *expr.AttributeExpr, pkg string, s *codegen.NameScope) string {
	switch actual := att.Type.(type) {
	case expr.UserType, expr.CompositeExpr:
		return protoBufFullMessageName(att, pkg, s)
	case expr.Primitive:
		return protoBufNativeGoTypeName(actual)
	case *expr.Array:
		return "[]" + protoBufGoFullTypeRef(actual.ElemType, pkg, s)
	case *expr.Map:
		return fmt.Sprintf("map[%s]%s",
			protoBufGoFullTypeRef(actual.KeyType, pkg, s),
			protoBufGoFullTypeRef(actual.ElemType, pkg, s))
	case *expr.Object:
		return s.GoTypeDef(att, false, false)
	default:
		panic(fmt.Sprintf("unknown data type %T", actual)) // bug
	}
}

// protoBufMessageDef returns the protocol buffer code that defines a message
// which matches the data structure definition (the part that comes after
// `message foo`). The message is defined using the proto3 syntax.
func protoBufMessageDef(att *expr.AttributeExpr, s *codegen.NameScope) string {
	switch actual := att.Type.(type) {
	case expr.Primitive:
		return protoBufNativeMessageTypeName(att.Type)
	case *expr.Array:
		return "repeated " + protoBufMessageDef(actual.ElemType, s)
	case *expr.Map:
		return fmt.Sprintf("map<%s, %s>", protoBufMessageDef(actual.KeyType, s), protoBufMessageDef(actual.ElemType, s))
	case expr.UserType:
		return protoBufMessageName(att, s)
	case *expr.Object:
		var ss []string
		ss = append(ss, " {")
		for _, nat := range *actual {
			var (
				fn   string
				fnum uint64
				typ  string
				desc string
			)
			{
				fn = codegen.SnakeCase(protoBufify(nat.Name, false))
				fnum = rpcTag(nat.Attribute)
				typ = protoBufMessageDef(nat.Attribute, s)
				if nat.Attribute.Description != "" {
					desc = codegen.Comment(nat.Attribute.Description) + "\n\t"
				}
			}
			ss = append(ss, fmt.Sprintf("\t%s%s %s = %d;", desc, typ, fn, fnum))
		}
		ss = append(ss, "}")
		return strings.Join(ss, "\n")
	default:
		panic(fmt.Sprintf("unknown data type %T", actual)) // bug
	}
}

// protoBufGoFullTypeRef returns the Go code qualified with package name that
// refers to the Go type generated by compiling the protocol buffer
// (in *.pb.go) for the given attribute.
func protoBufGoFullTypeRef(att *expr.AttributeExpr, pkg string, s *codegen.NameScope) string {
	name := protoBufGoFullTypeName(att, pkg, s)
	if expr.IsObject(att.Type) {
		return "*" + name
	}
	return name
}

// protoBufify makes a valid protocol buffer identifier out of any string.
// It does that by removing any non letter and non digit character and by
// making sure the first character is a letter or "_". protoBufify produces a
// "CamelCase" version of the string.
//
// If firstUpper is true the first character of the identifier is uppercase
// otherwise it's lowercase.
func protoBufify(str string, firstUpper bool) string {
	// Optimize trivial case
	if str == "" {
		return ""
	}

	// Remove optional suffix that defines corresponding transport specific
	// name.
	idx := strings.Index(str, ":")
	if idx > 0 {
		str = str[:idx]
	}

	// We must disable acronyms to generate gRPC compatible names.
	str = codegen.CamelCase(str, firstUpper, false)
	if str == "" {
		// All characters are invalid. Produce a default value.
		if firstUpper {
			return "Val"
		}
		return "val"
	}
	return fixReservedProtoBuf(str)
}

// protoBufifyAtt honors any struct:field:name meta set on the attribute and
// and calls protoBufify with the tag value if present or the given name
// otherwise.
func protoBufifyAtt(att *expr.AttributeExpr, name string, upper bool) string {
	if tname, ok := att.Meta["struct:field:name"]; ok {
		if len(tname) > 0 {
			name = tname[0]
		}
	}
	return protoBufify(name, upper)
}

// protoBufNativeMessageTypeName returns the protocol buffer built-in type
// corresponding to the given primitive type. It panics if t is not a
// primitive type.
func protoBufNativeMessageTypeName(t expr.DataType) string {
	switch t.Kind() {
	case expr.BooleanKind:
		return "bool"
	case expr.IntKind:
		return "sint32"
	case expr.Int32Kind:
		return "sint32"
	case expr.Int64Kind:
		return "sint64"
	case expr.UIntKind:
		return "uint32"
	case expr.UInt32Kind:
		return "uint32"
	case expr.UInt64Kind:
		return "uint64"
	case expr.Float32Kind:
		return "float"
	case expr.Float64Kind:
		return "double"
	case expr.StringKind:
		return "string"
	case expr.BytesKind:
		return "bytes"
	default:
		panic(fmt.Sprintf("cannot compute native protocol buffer type for %T", t)) // bug
	}
}

// protoBufNativeGoTypeName returns the Go type corresponding to the given
// primitive type generated by the protocol buffer compiler after compiling
// the ".proto" file (in *.pb.go).
func protoBufNativeGoTypeName(t expr.DataType) string {
	switch t.Kind() {
	case expr.BooleanKind:
		return "bool"
	case expr.IntKind:
		return "int32"
	case expr.Int32Kind:
		return "int32"
	case expr.Int64Kind:
		return "int64"
	case expr.UIntKind:
		return "uint32"
	case expr.UInt32Kind:
		return "uint32"
	case expr.UInt64Kind:
		return "uint64"
	case expr.Float32Kind:
		return "float32"
	case expr.Float64Kind:
		return "float64"
	case expr.StringKind:
		return "string"
	case expr.BytesKind:
		return "[]byte"
	default:
		panic(fmt.Sprintf("cannot compute native protocol buffer type for %T", t)) // bug
	}
}

// rpcTag returns the unique numbered RPC tag from the given attribute.
func rpcTag(a *expr.AttributeExpr) uint64 {
	var tag uint64
	if t, ok := a.Meta["rpc:tag"]; ok {
		tn, err := strconv.ParseUint(t[0], 10, 64)
		if err != nil {
			panic(err) // bug (should catch invalid field numbers in validation)
		}
		tag = tn
	}
	return tag
}

// fixReservedProtoBuf appends an underscore on to protocol buffer reserved
// keywords.
func fixReservedProtoBuf(w string) string {
	if _, ok := reservedProtoBuf[codegen.CamelCase(w, false, false)]; ok {
		w += "_"
	}
	return w
}

var (
	// reserved protocol buffer keywords and package names
	reservedProtoBuf = map[string]struct{}{
		// types
		"bool":     struct{}{},
		"bytes":    struct{}{},
		"double":   struct{}{},
		"fixed32":  struct{}{},
		"fixed64":  struct{}{},
		"float":    struct{}{},
		"int32":    struct{}{},
		"int64":    struct{}{},
		"sfixed32": struct{}{},
		"sfixed64": struct{}{},
		"sint32":   struct{}{},
		"sint64":   struct{}{},
		"string":   struct{}{},
		"uint32":   struct{}{},
		"uint64":   struct{}{},

		// reserved keywords
		"enum":     struct{}{},
		"import":   struct{}{},
		"map":      struct{}{},
		"message":  struct{}{},
		"oneof":    struct{}{},
		"option":   struct{}{},
		"package":  struct{}{},
		"public":   struct{}{},
		"repeated": struct{}{},
		"reserved": struct{}{},
		"returns":  struct{}{},
		"rpc":      struct{}{},
		"service":  struct{}{},
		"syntax":   struct{}{},
	}
)
