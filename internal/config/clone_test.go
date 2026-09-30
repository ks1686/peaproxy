package config

import (
	"reflect"
	"testing"
)

// populate fills v so every pointer, slice and map at every depth is non-nil
// and every scalar is non-zero.
func populate(v reflect.Value) {
	switch v.Kind() {
	case reflect.Ptr:
		p := reflect.New(v.Type().Elem())
		populate(p.Elem())
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		populate(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		populate(key)
		val := reflect.New(v.Type().Elem()).Elem()
		populate(val)
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				populate(v.Field(i))
			}
		}
	case reflect.String:
		v.SetString("k")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	}
}

func assertDistinct(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	switch a.Kind() {
	case reflect.Ptr:
		if a.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Errorf("%s aliases the original", path)
		}
		assertDistinct(t, a.Elem(), b.Elem(), "*"+path)
	case reflect.Slice:
		if a.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Errorf("%s aliases the original", path)
		}
		for i := 0; i < a.Len() && i < b.Len(); i++ {
			assertDistinct(t, a.Index(i), b.Index(i), path+"[i]")
		}
	case reflect.Map:
		if a.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Errorf("%s aliases the original", path)
		}
		for _, k := range a.MapKeys() {
			bv := b.MapIndex(k)
			if !bv.IsValid() {
				continue
			}
			assertDistinct(t, a.MapIndex(k), bv, path+"[k]")
		}
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			assertDistinct(t, a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name)
		}
	}
}

func TestCloneCoversEveryField(t *testing.T) {
	var orig Config
	populate(reflect.ValueOf(&orig).Elem())
	if orig.AutomaticRoutes.Prices["k"].Input == nil || orig.Providers[0].OAuth.Extra == nil || orig.Failover.SessionAffinity == nil {
		t.Fatal("populate left a nested pointer or map nil")
	}
	cp := Clone(orig)
	if !reflect.DeepEqual(orig, cp) {
		t.Fatalf("clone differs from original:\n%#v\n%#v", orig, cp)
	}
	assertDistinct(t, reflect.ValueOf(orig), reflect.ValueOf(cp), "Config")
}

func TestCloneIsDeep(t *testing.T) {
	var orig Config
	populate(reflect.ValueOf(&orig).Elem())
	cp := Clone(orig)

	*cp.AutomaticRoutes.Prices["k"].Input = 42
	cp.Providers[0].OAuth.Extra["x"] = "y"
	cp.Providers[0].OAuth.AccessToken = "changed"
	cp.Routes["r"] = "t"
	cp.AutomaticRoutes.Auto[0] = "changed"
	cp.Hide.Models[0] = "changed"
	cp.Catalog.Rename["k"] = "changed"
	*cp.Failover.SessionAffinity = false

	if *orig.AutomaticRoutes.Prices["k"].Input != 1 {
		t.Fatal("Prices value pointer shared")
	}
	if _, ok := orig.Providers[0].OAuth.Extra["x"]; ok || orig.Providers[0].OAuth.AccessToken != "k" {
		t.Fatal("OAuth shared")
	}
	if _, ok := orig.Routes["r"]; ok {
		t.Fatal("Routes shared")
	}
	if orig.AutomaticRoutes.Auto[0] != "k" || orig.Hide.Models[0] != "k" || orig.Catalog.Rename["k"] != "k" {
		t.Fatal("slice or map shared")
	}
	if !*orig.Failover.SessionAffinity {
		t.Fatal("SessionAffinity shared")
	}
}

func TestClonePreservesNilAndEmpty(t *testing.T) {
	c := Config{Hide: HideList{Models: []string{}}, Routes: map[string]string{}}
	cp := Clone(c)
	if cp.Hide.Providers != nil || cp.Hide.Models == nil || cp.Routes == nil || cp.Catalog.Rename != nil {
		t.Fatalf("nil/empty not preserved: %#v", cp)
	}
}
