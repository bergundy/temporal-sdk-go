package binding

import (
	"strings"

	commonpb "go.temporal.io/api/common/v1"
	nexuspb "go.temporal.io/api/nexus/v1"
	_ "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
)

var (
	headerName       = (&commonpb.Header{}).ProtoReflect().Descriptor().FullName()
	anyName          = (&anypb.Any{}).ProtoReflect().Descriptor().FullName()
	nexusRequestName = (&nexuspb.Request{}).ProtoReflect().Descriptor().FullName()

	// index lists, for each message type that can reach a header, the fields
	// that can: Temporal headers, Nexus header maps, Any bodies, and messages
	// that lead to one of those. Built once, it keeps the walk off the bulk of
	// a request, such as payloads.
	index = buildIndex()
)

// isNexusHeaderField reports whether fd is a Nexus header map: a
// map<string, string> named nexus_header, or nexus.v1.Request.header.
func isNexusHeaderField(fd protoreflect.FieldDescriptor) bool {
	if !fd.IsMap() || fd.MapKey().Kind() != protoreflect.StringKind || fd.MapValue().Kind() != protoreflect.StringKind {
		return false
	}
	return fd.Name() == "nexus_header" || (fd.Name() == "header" && fd.ContainingMessage().FullName() == nexusRequestName)
}

func fieldMessage(fd protoreflect.FieldDescriptor) protoreflect.MessageDescriptor {
	if fd.IsMap() {
		if fd.MapValue().Kind() == protoreflect.MessageKind {
			return fd.MapValue().Message()
		}
		return nil
	}
	if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
		return fd.Message()
	}
	return nil
}

// apiMessages returns every message type in a temporal.api package and every
// type those reach through their fields.
func apiMessages() map[protoreflect.FullName]protoreflect.MessageDescriptor {
	all := map[protoreflect.FullName]protoreflect.MessageDescriptor{}
	var add func(md protoreflect.MessageDescriptor)
	add = func(md protoreflect.MessageDescriptor) {
		if _, ok := all[md.FullName()]; ok || md.IsMapEntry() {
			return
		}
		all[md.FullName()] = md
		fields := md.Fields()
		for i := range fields.Len() {
			if m := fieldMessage(fields.Get(i)); m != nil {
				add(m)
			}
		}
		nested := md.Messages()
		for i := range nested.Len() {
			add(nested.Get(i))
		}
	}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if strings.HasPrefix(string(fd.Package()), "temporal.api.") {
			msgs := fd.Messages()
			for i := range msgs.Len() {
				add(msgs.Get(i))
			}
		}
		return true
	})
	return all
}

func buildIndex() map[protoreflect.FullName][]protoreflect.FieldDescriptor {
	all := apiMessages()
	reaches := map[protoreflect.FullName]bool{headerName: true, anyName: true}
	leads := func(fd protoreflect.FieldDescriptor) bool {
		if isNexusHeaderField(fd) {
			return true
		}
		m := fieldMessage(fd)
		return m != nil && reaches[m.FullName()]
	}
	for changed := true; changed; {
		changed = false
		for name, md := range all {
			if reaches[name] {
				continue
			}
			fields := md.Fields()
			for i := range fields.Len() {
				if leads(fields.Get(i)) {
					reaches[name] = true
					changed = true
					break
				}
			}
		}
	}
	idx := map[protoreflect.FullName][]protoreflect.FieldDescriptor{}
	for name, md := range all {
		if !reaches[name] || name == headerName || name == anyName {
			continue
		}
		var fds []protoreflect.FieldDescriptor
		fields := md.Fields()
		for i := range fields.Len() {
			if fd := fields.Get(i); leads(fd) {
				fds = append(fds, fd)
			}
		}
		idx[name] = fds
	}
	return idx
}

// handled is the set of positions a pass sealed or opened. The strip pass
// leaves them alone and cleans every other one.
type handled struct {
	headers map[*commonpb.Header]struct{}
	nexus   map[proto.Message]struct{} // keyed by the message holding the Nexus header
	anys    map[*anypb.Any]struct{}    // Any bodies already edited and re-packed
}

func newHandled() *handled {
	return &handled{
		headers: map[*commonpb.Header]struct{}{},
		nexus:   map[proto.Message]struct{}{},
		anys:    map[*anypb.Any]struct{}{},
	}
}

// visitor is called for each header the walk reaches. Each method reports
// whether it changed what it was given, so edited Any bodies are re-packed.
type visitor interface {
	header(h *commonpb.Header) bool
	nexus(parent protoreflect.Message, h protoreflect.Map) bool
	skipAny(a *anypb.Any) bool
}

// walk visits every Temporal header, Nexus header map, and Any body holding a
// Temporal API type in m.
func walk(m protoreflect.Message, v visitor) bool {
	name := m.Descriptor().FullName()
	switch name {
	case headerName:
		h, ok := m.Interface().(*commonpb.Header)
		return ok && v.header(h)
	case anyName:
		return walkAny(m, v)
	}
	fields, ok := index[name]
	if !ok {
		return false
	}
	changed := false
	for _, fd := range fields {
		if !m.Has(fd) {
			continue
		}
		switch {
		case isNexusHeaderField(fd):
			changed = v.nexus(m, m.Mutable(fd).Map()) || changed
		case fd.IsMap():
			m.Get(fd).Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				changed = walk(mv.Message(), v) || changed
				return true
			})
		case fd.IsList():
			l := m.Get(fd).List()
			for j := range l.Len() {
				changed = walk(l.Get(j).Message(), v) || changed
			}
		default:
			changed = walk(m.Get(fd).Message(), v) || changed
		}
	}
	return changed
}

func walkAny(m protoreflect.Message, v visitor) bool {
	a, ok := m.Interface().(*anypb.Any)
	if !ok || v.skipAny(a) {
		return false
	}
	body, ok := unpackAPI(a)
	if !ok {
		return false
	}
	if !walk(body.ProtoReflect(), v) {
		return false
	}
	return repack(a, body)
}

// unpackAPI unpacks an Any holding a Temporal API type. Other types can't
// carry a header.
func unpackAPI(a *anypb.Any) (proto.Message, bool) {
	name := a.MessageName()
	if !strings.HasPrefix(string(name), "temporal.api.") {
		return nil, false
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(name)
	if err != nil {
		return nil, false
	}
	body := mt.New().Interface()
	if err := proto.Unmarshal(a.GetValue(), body); err != nil {
		return nil, false
	}
	return body, true
}

func repack(a *anypb.Any, body proto.Message) bool {
	b, err := proto.Marshal(body)
	if err != nil {
		return false
	}
	a.Value = b
	return true
}

// Nexus header keys are case-insensitive.

func isFamilyKey(k string) bool {
	return strings.EqualFold(k, HeaderCredentials) || strings.EqualFold(k, HeaderKey) || strings.EqualFold(k, HeaderOpened)
}

// nexusFamilyKeys returns the credentials-family keys in h, in any case.
func nexusFamilyKeys(h protoreflect.Map) []protoreflect.MapKey {
	var keys []protoreflect.MapKey
	h.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
		if isFamilyKey(k.String()) {
			keys = append(keys, k)
		}
		return true
	})
	return keys
}

// scan reports whether any header in a message carries a credentials field.
type scan struct{ found bool }

func (s *scan) header(h *commonpb.Header) bool {
	for k := range h.GetFields() {
		if k == HeaderCredentials || k == HeaderKey || k == HeaderOpened {
			s.found = true
		}
	}
	return false
}

func (s *scan) nexus(_ protoreflect.Message, h protoreflect.Map) bool {
	if len(nexusFamilyKeys(h)) > 0 {
		s.found = true
	}
	return false
}

func (s *scan) skipAny(*anypb.Any) bool { return s.found }

// HasCredentials reports whether any Temporal header, Nexus header, or Any
// body in m carries a field of the credentials family.
func HasCredentials(m proto.Message) bool {
	if m == nil {
		return false
	}
	s := &scan{}
	walk(m.ProtoReflect(), s)
	return s.found
}
