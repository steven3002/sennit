package mcp

// ResourceLinkForTest is resourceLink, for the package's external tests.
//
// Through the protocol, the guard on a link's name is reached only by a record
// with nothing to call it, and a stored record is refused a blank title or
// statement, so the blank and whitespace-only names the guard exists for are
// given to it directly here instead.
var ResourceLinkForTest = resourceLink
