// Package private makes a file readable by the account that runs Sennit and by
// nobody else, on every platform it ships for.
//
// It is one call because the two platforms disagree about what the question
// even is. On Unix a file mode answers it. On Windows a mode means nothing past
// the read-only bit, and who may open a file is decided by its access list,
// which a new file takes from the folder it is made in.
package private
