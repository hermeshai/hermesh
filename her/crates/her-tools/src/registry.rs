//! Ordered tool registry. The order is what the model sees, which keeps prompts cache-friendly.

use her_llm::ToolSpec;
use std::collections::HashMap;
use std::sync::Arc;

use crate::tool::Tool;

#[derive(Debug, thiserror::Error)]
pub enum RegistryError {
    #[error("invalid tool name '{0}': use 1-64 letters, digits, '_' or '-'")]
    InvalidName(String),
    #[error("duplicate tool name '{0}'")]
    Duplicate(String),
    #[error("unknown tool '{name}' (available: {available})")]
    Unknown { name: String, available: String },
}

#[derive(Default, Clone)]
pub struct ToolRegistry {
    tools: Vec<Arc<dyn Tool>>,
    index: HashMap<String, usize>,
}

impl ToolRegistry {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn register(&mut self, tool: Arc<dyn Tool>) -> Result<(), RegistryError> {
        let name = tool.spec().name.clone();
        let valid = !name.is_empty()
            && name.len() <= 64
            && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-');
        if !valid {
            return Err(RegistryError::InvalidName(name));
        }
        if self.index.contains_key(&name) {
            return Err(RegistryError::Duplicate(name));
        }
        self.index.insert(name, self.tools.len());
        self.tools.push(tool);
        Ok(())
    }

    pub fn get(&self, name: &str) -> Option<Arc<dyn Tool>> {
        self.index.get(name).map(|i| self.tools[*i].clone())
    }

    pub fn names(&self) -> Vec<String> {
        self.tools.iter().map(|t| t.spec().name.clone()).collect()
    }

    pub fn specs(&self) -> Vec<ToolSpec> {
        self.iter_specs().cloned().collect()
    }

    /// Borrow definitions for comparison and accounting without copying their JSON schemas.
    pub fn iter_specs(&self) -> impl ExactSizeIterator<Item = &ToolSpec> {
        self.tools.iter().map(|t| t.spec())
    }

    pub fn is_empty(&self) -> bool {
        self.tools.is_empty()
    }

    /// The listed tools, in the order given.
    pub fn subset(&self, names: &[String]) -> Result<ToolRegistry, RegistryError> {
        let mut out = ToolRegistry::new();
        for name in names {
            let tool = self.get(name).ok_or_else(|| RegistryError::Unknown {
                name: name.clone(),
                available: self.names().join(", "),
            })?;
            out.register(tool)?;
        }
        Ok(out)
    }

    /// Guidelines of all tools, in tool order, without duplicates.
    pub fn guidelines(&self) -> Vec<&'static str> {
        let mut out: Vec<&'static str> = Vec::new();
        for tool in &self.tools {
            for line in tool.guidelines() {
                if !out.contains(line) {
                    out.push(line);
                }
            }
        }
        out
    }
}
