// Code generated by mockery v2.53.4. DO NOT EDIT.

package mocks

import (
	interfaces "github.com/behrlich/wingthing/internal/interfaces"
	mock "github.com/stretchr/testify/mock"
)

// PermissionChecker is an autogenerated mock type for the PermissionChecker type
type PermissionChecker struct {
	mock.Mock
}

// CheckPermission provides a mock function with given fields: tool, action, params
func (_m *PermissionChecker) CheckPermission(tool string, action string, params map[string]interface{}) (bool, error) {
	ret := _m.Called(tool, action, params)

	if len(ret) == 0 {
		panic("no return value specified for CheckPermission")
	}

	var r0 bool
	var r1 error
	if rf, ok := ret.Get(0).(func(string, string, map[string]interface{}) (bool, error)); ok {
		return rf(tool, action, params)
	}
	if rf, ok := ret.Get(0).(func(string, string, map[string]interface{}) bool); ok {
		r0 = rf(tool, action, params)
	} else {
		r0 = ret.Get(0).(bool)
	}

	if rf, ok := ret.Get(1).(func(string, string, map[string]interface{}) error); ok {
		r1 = rf(tool, action, params)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// DenyPermission provides a mock function with given fields: tool, action, params, decision
func (_m *PermissionChecker) DenyPermission(tool string, action string, params map[string]interface{}, decision interfaces.PermissionDecision) {
	_m.Called(tool, action, params, decision)
}

// GrantPermission provides a mock function with given fields: tool, action, params, decision
func (_m *PermissionChecker) GrantPermission(tool string, action string, params map[string]interface{}, decision interfaces.PermissionDecision) {
	_m.Called(tool, action, params, decision)
}

// LoadFromFile provides a mock function with given fields: filePath
func (_m *PermissionChecker) LoadFromFile(filePath string) error {
	ret := _m.Called(filePath)

	if len(ret) == 0 {
		panic("no return value specified for LoadFromFile")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(filePath)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// SaveToFile provides a mock function with given fields: filePath
func (_m *PermissionChecker) SaveToFile(filePath string) error {
	ret := _m.Called(filePath)

	if len(ret) == 0 {
		panic("no return value specified for SaveToFile")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(filePath)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// NewPermissionChecker creates a new instance of PermissionChecker. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewPermissionChecker(t interface {
	mock.TestingT
	Cleanup(func())
}) *PermissionChecker {
	mock := &PermissionChecker{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
