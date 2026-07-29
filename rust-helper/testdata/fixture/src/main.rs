macro_rules! macro_caller { () => { macro_target() } }

fn main() {
    live();
    let t: &dyn Speak = &Dog;
    t.speak();
    gen_call(42i32);
    macro_caller!();
}
fn live() { helper(); }
fn helper() {}
fn dead() {}
fn only_test() {}
trait Speak { fn speak(&self); }
struct Dog;
impl Speak for Dog { fn speak(&self) { dyn_target(); } }
fn dyn_target() {}
fn gen_call<T: std::fmt::Debug>(_v: T) { generic_target(); }
fn generic_target() {}
fn macro_target() {}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn t() { only_test(); }
}
