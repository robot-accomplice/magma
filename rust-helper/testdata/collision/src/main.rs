//! Task 13 collision probe: two traits/types both bare-named `Amb`/`Widget`
//! in different modules — one side entirely dead, the other live except for
//! one genuinely-unused method. A name-keyed cascade exclusion (the pre-
//! Task-13 harness) treats `dead_traits = ["Amb"]` / `dead_types = ["Widget"]`
//! (both from `dead_side`) as covering `live_side::Amb`'s impl too, purely on
//! name coincidence, and wrongly excludes `live_side::Widget::never_used`
//! from the oracle-diff comparison even though rustc gives it its own
//! per-method "never used" diagnostic. The (file, line)-keyed gate must not.

mod dead_side {
    pub trait Amb {
        fn dead_method(&self);
    }
    pub struct Widget;
    impl Amb for Widget {
        fn dead_method(&self) {}
    }
}

mod live_side {
    pub trait Amb {
        fn live_method(&self);
        fn never_used(&self);
    }
    pub struct Widget;
    impl Amb for Widget {
        fn live_method(&self) {}
        fn never_used(&self) {}
    }
}

fn main() {
    use live_side::Amb as _;
    let w = live_side::Widget;
    w.live_method();
}
