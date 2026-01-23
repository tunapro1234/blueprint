use std::sync::Mutex;

pub struct Rotator {
    keys: Vec<String>,
    next: Mutex<usize>,
}

impl Rotator {
    pub fn new(keys: Vec<String>) -> Self {
        Self {
            keys,
            next: Mutex::new(0),
        }
    }

    pub fn next(&self) -> Option<String> {
        if self.keys.is_empty() {
            return None;
        }
        let mut idx = self.next.lock().ok()?;
        let key = self.keys[*idx % self.keys.len()].clone();
        *idx += 1;
        Some(key)
    }
}
