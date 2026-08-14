package api

import (
	"reflect"
	"testing"
)

func TestJSONScannersAcceptSQLiteBytesAndPostgresStrings(t *testing.T) {
	for _, value := range []any{[]byte(`{"gpu":"up"}`), `{"gpu":"up"}`} {
		var result StringMap
		if err := result.Scan(value); err != nil {
			t.Fatalf("scan StringMap from %T: %v", value, err)
		}
		if !reflect.DeepEqual(result, StringMap{"gpu": "up"}) {
			t.Fatalf("unexpected StringMap from %T: %#v", value, result)
		}
	}

	for _, value := range []any{[]byte(`["a","b"]`), `["a","b"]`} {
		var result StringList
		if err := result.Scan(value); err != nil {
			t.Fatalf("scan StringList from %T: %v", value, err)
		}
		if !reflect.DeepEqual(result, StringList{"a", "b"}) {
			t.Fatalf("unexpected StringList from %T: %#v", value, result)
		}
	}

	for _, value := range []any{[]byte(`{"temperature":42.5}`), `{"temperature":42.5}`} {
		var result FloatMap
		if err := result.Scan(value); err != nil {
			t.Fatalf("scan FloatMap from %T: %v", value, err)
		}
		if !reflect.DeepEqual(result, FloatMap{"temperature": 42.5}) {
			t.Fatalf("unexpected FloatMap from %T: %#v", value, result)
		}
	}

	for _, value := range []any{[]byte(`[0.1,0.2,0.7]`), `[0.1,0.2,0.7]`} {
		var result FloatList
		if err := result.Scan(value); err != nil {
			t.Fatalf("scan FloatList from %T: %v", value, err)
		}
		if !reflect.DeepEqual(result, FloatList{0.1, 0.2, 0.7}) {
			t.Fatalf("unexpected FloatList from %T: %#v", value, result)
		}
	}
}

func TestJSONScannersRejectUnsupportedValues(t *testing.T) {
	var result StringMap
	if err := result.Scan(42); err == nil {
		t.Fatal("expected unsupported database value to fail")
	}
}
