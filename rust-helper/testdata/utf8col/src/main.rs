//! Task 17 probe: dead functions preceded, on their own declaration line, by
//! non-ASCII text — the case where ra_ap's UTF-8 *byte* column and rustc's
//! *character* column diverge. `line-index-0.1.2` documents its `col` as
//! "Zero-based UTF-8 offset"; rustc counts Unicode scalar values. The two
//! agree on ASCII lines and diverge on any line with multi-byte UTF-8
//! content — before the fix this misses the oracle key and surfaces as a
//! FATAL even though rustc also reports the function dead.
//!
//! Two cases, both dead (never called from `main`):
//!   - `dead_fn`: preceded by a CJK comment (3-byte-per-char UTF-8, still
//!     within the BMP — one UTF-16 code unit each).
//!   - `dead_emoji_fn`: preceded by an emoji comment (4-byte-per-char UTF-8,
//!     OUTSIDE the BMP — a UTF-16 *surrogate pair*, two code units). A
//!     UTF-16-based conversion would undercount this by one per emoji;
//!     rustc's character count does not, so the fix must not either.

/* 日本語コメント */ fn dead_fn() {}

/* 🎉🎉 emoji comment */ fn dead_emoji_fn() {}

fn main() {
    println!("utf8col fixture");
}
