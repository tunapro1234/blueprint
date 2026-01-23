use std::collections::HashMap;
use std::fs;
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::RwLock;

use chrono::Utc;
use super::types::{Task, TaskStatus};

static COUNTER: AtomicUsize = AtomicUsize::new(1);

pub struct TaskStore {
    persist: bool,
    path: Option<PathBuf>,
    tasks: RwLock<HashMap<String, Task>>,
}

impl TaskStore {
    pub fn new(persist: bool, path: Option<PathBuf>) -> Self {
        Self {
            persist,
            path,
            tasks: RwLock::new(HashMap::new()),
        }
    }

    pub fn create(&self, instruction: &str) -> Task {
        let id = next_id();
        let task = Task {
            id: id.clone(),
            instruction: instruction.to_string(),
            status: TaskStatus::Pending,
            output: None,
            error: None,
            created_at: Utc::now(),
            completed_at: None,
        };
        if let Ok(mut map) = self.tasks.write() {
            map.insert(id, task.clone());
        }
        self.save_if_needed();
        task
    }

    pub fn update(
        &self,
        id: &str,
        status: TaskStatus,
        output: Option<String>,
        error: Option<String>,
    ) -> Option<Task> {
        let mut updated = None;
        if let Ok(mut map) = self.tasks.write() {
            if let Some(task) = map.get_mut(id) {
                task.status = status;
                if output.is_some() {
                    task.output = output;
                }
                if error.is_some() {
                    task.error = error;
                }
                if matches!(task.status, TaskStatus::Completed | TaskStatus::Failed) {
                    task.completed_at = Some(Utc::now());
                }
                updated = Some(task.clone());
            }
        }
        self.save_if_needed();
        updated
    }

    pub fn get(&self, id: &str) -> Option<Task> {
        let map = self.tasks.read().ok()?;
        map.get(id).cloned()
    }

    pub fn list(&self, limit: usize) -> Vec<Task> {
        let map = match self.tasks.read() {
            Ok(lock) => lock,
            Err(_) => return vec![],
        };
        let mut items: Vec<Task> = map.values().cloned().collect();
        items.sort_by(|a, b| b.created_at.cmp(&a.created_at));
        if items.len() > limit {
            items.truncate(limit);
        }
        items
    }

    fn save_if_needed(&self) {
        if !self.persist {
            return;
        }
        let path = match &self.path {
            Some(path) => path.clone(),
            None => return,
        };
        let map = match self.tasks.read() {
            Ok(lock) => lock,
            Err(_) => return,
        };
        let list: Vec<&Task> = map.values().collect();
        if let Ok(serialized) = serde_json::to_string_pretty(&list) {
            let _ = fs::write(path, serialized);
        }
    }

    pub fn load_from_disk(path: PathBuf) -> Option<Vec<Task>> {
        let data = fs::read_to_string(path).ok()?;
        serde_json::from_str::<Vec<Task>>(&data).ok()
    }
}

fn next_id() -> String {
    let count = COUNTER.fetch_add(1, Ordering::SeqCst);
    format!("task_{}_{}", Utc::now().timestamp_millis(), count)
}
