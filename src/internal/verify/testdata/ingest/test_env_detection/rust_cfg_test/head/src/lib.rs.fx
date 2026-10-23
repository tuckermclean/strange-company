pub fn f() -> u32 {
    if cfg!(test) {
        1
    } else {
        0
    }
}
