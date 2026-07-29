trait Speak { fn speak(&self); }
struct Dog;
struct Cat;
impl Speak for Dog { fn speak(&self) { dog_target(); } }
impl Speak for Cat { fn speak(&self) { cat_target(); } }
fn dog_target() {}
fn cat_target() {}
fn main() {
    let a: &dyn Speak = &Dog;
    a.speak();          // could be Dog::speak OR Cat::speak at runtime
    let _c = Cat;       // Cat is constructed, so rustc considers its impl live
}
