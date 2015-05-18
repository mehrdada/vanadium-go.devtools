// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"go/build"
	"testing"
)

func allow(expr string) importRule   { return importRule{false, expr} }
func deny(expr string) importRule    { return importRule{true, expr} }
func pkg(path string) *build.Package { return &build.Package{ImportPath: path} }
func pkgGoroot(path string) *build.Package {
	p := pkg(path)
	p.Goroot = true
	return p
}

func TestEnforceImportRule(t *testing.T) {
	tests := []struct {
		rule   importRule
		pkg    *build.Package
		result importResult
	}{
		{deny("..."), pkg("foo"), rejectedResult},
		{deny("..."), pkgGoroot("foo"), undecidedResult},
		{allow("..."), pkg("foo"), approvedResult},
		{allow("..."), pkgGoroot("foo"), undecidedResult},

		{deny("foo"), pkg("foo"), rejectedResult},
		{deny("foo"), pkg("foo/a"), undecidedResult},
		{deny("foo"), pkg("bar"), undecidedResult},
		{allow("foo"), pkg("foo"), approvedResult},
		{allow("foo"), pkg("foo/a"), undecidedResult},
		{allow("foo"), pkg("bar"), undecidedResult},

		{deny("foo/..."), pkg("foo"), rejectedResult},
		{deny("foo/..."), pkg("foo/a"), rejectedResult},
		{deny("foo/..."), pkg("foo/a/b/c"), rejectedResult},
		{deny("foo/..."), pkg("bar"), undecidedResult},
		{deny("foo/..."), pkg("bar/foo"), undecidedResult},
		{allow("foo/..."), pkg("foo"), approvedResult},
		{allow("foo/..."), pkg("foo/a"), approvedResult},
		{allow("foo/..."), pkg("foo/a/b/c"), approvedResult},
		{allow("foo/..."), pkg("bar"), undecidedResult},
		{allow("foo/..."), pkg("bar/foo"), undecidedResult},
	}
	for _, test := range tests {
		result, err := test.rule.enforce(test.pkg)
		if err != nil {
			t.Errorf("%v %s failed: %v", test.rule, test.pkg.ImportPath, err)
		}
		if got, want := result, test.result; got != want {
			t.Errorf("%v %s got %v, want %v", test.rule, test.pkg.ImportPath, got, want)
		}
	}
}

func TestVerifyGo15InternalRule(t *testing.T) {
	tests := []struct {
		src, dst string
		want     bool
	}{
		{"foo", "bar", true},
		// Anything rooted at "a/b/c" is allowed.
		{"a/b/c", "a/b/c/internal", true},
		{"a/b/c/X", "a/b/c/internal", true},
		{"a/b/c/X/Y", "a/b/c/internal", true},
		{"a/b/c", "a/b/c/internal/d/e/f", true},
		{"a/b/c/X", "a/b/c/internal/d/e/f", true},
		{"a/b/c/X/Y", "a/b/c/internal/d/e/f", true},
		// Things not rooted at "a/b/c" are rejected.
		{"a", "a/b/c/internal", false},
		{"z", "a/b/c/internal", false},
		{"a/b", "a/b/c/internal", false},
		{"a/b/X", "a/b/c/internal", false},
		{"a/b/X/Y", "a/b/c/internal", false},
		{"a/b/ccc", "a/b/c/internal", false},
		{"a", "a/b/c/internal/d/e/f", false},
		{"z", "a/b/c/internal/d/e/f", false},
		{"a/b", "a/b/c/internal/d/e/f", false},
		{"a/b/X", "a/b/c/internal/d/e/f", false},
		{"a/b/X/Y", "a/b/c/internal/d/e/f", false},
		{"a/b/ccc", "a/b/c/internal/d/e/f", false},
		// The path component must be "internal".
		{"a", "a/b/c/intern", true},
		{"z", "a/b/c/intern", true},
		{"a/b", "a/b/c/intern", true},
		{"a/b/X", "a/b/c/intern", true},
		{"a/b/X/Y", "a/b/c/intern", true},
		{"a/b/ccc", "a/b/c/intern", true},
		{"a", "a/b/c/internalZ/d/e/f", true},
		{"z", "a/b/c/internalZ/d/e/f", true},
		{"a/b", "a/b/c/internalZ/d/e/f", true},
		{"a/b/X", "a/b/c/internalZ/d/e/f", true},
		{"a/b/X/Y", "a/b/c/internalZ/d/e/f", true},
		{"a/b/ccc", "a/b/c/internalZ/d/e/f", true},
		// Multiple internal, anything rooted at "a/b/c/internal/d/e/f" is allowed.
		{"a/b/c/internal/d/e/f", "a/b/c/internal/d/e/f/internal", true},
		{"a/b/c/internal/d/e/f/X", "a/b/c/internal/d/e/f/internal", true},
		{"a/b/c/internal/d/e/f/X/Y", "a/b/c/internal/d/e/f/internal", true},
		{"a/b/c/internal/d/e/f", "a/b/c/internal/d/e/f/internal/g/h/i", true},
		{"a/b/c/internal/d/e/f/X", "a/b/c/internal/d/e/f/internal/g/h/i", true},
		{"a/b/c/internal/d/e/f/X/Y", "a/b/c/internal/d/e/f/internal/g/h/i", true},
		// Multiple internal, things not rooted at "a/b/c/internal/d/e/f" are rejected.
		{"a/b/c", "a/b/c/internal/d/e/f/internal", false},
		{"a/b/c/X", "a/b/c/internal/d/e/f/internal", false},
		{"a/b/c/X/Y", "a/b/c/internal/d/e/f/internal", false},
		{"a/b/c/internal", "a/b/c/internal/d/e/f/internal", false},
		{"a/b/c/internal/d", "a/b/c/internal/d/e/f/internal", false},
		{"a/b/c/internal/d/e", "a/b/c/internal/d/e/f/internal", false},
		{"a/b/c/internal/d/e/X", "a/b/c/internal/d/e/f/internal", false},
		{"a/b/c/internal/d/e/X/Y", "a/b/c/internal/d/e/f/internal", false},
		{"a/b/c/internal/d/e/fff", "a/b/c/internal/d/e/f/internal", false},
		{"a/b/c", "a/b/c/internal/d/e/f/internal/g/h/i", false},
		{"a/b/c/X", "a/b/c/internal/d/e/f/internal/g/h/i", false},
		{"a/b/c/X/Y", "a/b/c/internal/d/e/f/internal/g/h/i", false},
		{"a/b/c/internal", "a/b/c/internal/d/e/f/internal/g/h/i", false},
		{"a/b/c/internal/d", "a/b/c/internal/d/e/f/internal/g/h/i", false},
		{"a/b/c/internal/d/e", "a/b/c/internal/d/e/f/internal/g/h/i", false},
		{"a/b/c/internal/d/e/X", "a/b/c/internal/d/e/f/internal/g/h/i", false},
		{"a/b/c/internal/d/e/X/Y", "a/b/c/internal/d/e/f/internal/g/h/i", false},
		{"a/b/c/internal/d/e/fff", "a/b/c/internal/d/e/f/internal/g/h/i", false},
	}
	for _, test := range tests {
		got, want := verifyGo15InternalRule(test.src, test.dst), test.want
		if got != want {
			t.Errorf("%v failed", test)
		}
	}
}

func TestCheckImports(t *testing.T) {
	tests := []struct {
		name string
		pass bool
	}{
		{"v.io/x/devtools/godepcop/testdata/test-a", true},
		{"v.io/x/devtools/godepcop/testdata/test-b", false},
		{"v.io/x/devtools/godepcop/testdata/test-c", false},
		{"v.io/x/devtools/godepcop/testdata/test-c/child", true},
		{"v.io/x/devtools/godepcop/testdata/import-C", true},
		{"v.io/x/devtools/godepcop/testdata/import-unsafe", true},
		{"v.io/x/devtools/godepcop/testdata/test-internal", true},
		{"v.io/x/devtools/godepcop/testdata/test-internal/child", true},
		{"v.io/x/devtools/godepcop/testdata/test-internal/internal/child", true},
		{"v.io/x/devtools/godepcop/testdata/test-internal-fail", false},
	}
	for _, test := range tests {
		p, err := importPackage(test.name)
		if err != nil {
			t.Errorf("%s error loading package: %v", test.name, err)
			continue
		}
		v, err := checkImports(p)
		if err != nil {
			t.Errorf("%s failed: %v", test.name, err)
			continue
		}
		if got, want := test.pass, len(v) == 0; got != want {
			t.Errorf("%s didn't %s as expected", test.name, map[bool]string{true: "pass", false: "fail"}[test.pass])
		}
	}
}
