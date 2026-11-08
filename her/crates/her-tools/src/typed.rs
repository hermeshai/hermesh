//! Typed tools: parameters are a struct, the schema is derived and arguments are validated by
//! deserialization.

use async_trait::async_trait;
use her_llm::ToolSpec;
use schemars::JsonSchema;
use serde::de::DeserializeOwned;
use serde_json::Value;
use serde_json::json;

use crate::env::Environment;
use crate::tool::Authority;
use crate::tool::Tool;
use crate::tool::ToolCtx;
use crate::tool::ToolDescription;
use crate::tool::ToolError;
use crate::tool::ToolKind;
use crate::tool::ToolOutput;

#[async_trait]
pub trait TypedTool: Send + Sync + 'static {
    type Args: DeserializeOwned + JsonSchema + Send + Sync + 'static;
    const NAME: &'static str;
    const DESCRIPTION: &'static str;

    fn kind(&self) -> ToolKind {
        ToolKind::ReadOnly
    }

    /// See [`Tool::kind_for`]. Receives the raw arguments.
    fn kind_for(&self, args: &Value) -> ToolKind {
        let _ = args;
        self.kind()
    }

    fn guidelines(&self) -> &[&'static str] {
        &[]
    }

    /// See [`Tool::authorities`].
    async fn authorities(&self, args: &Self::Args, ctx: &ToolCtx) -> Result<Vec<Authority>, ToolError> {
        let _ = (args, ctx);
        Ok(Vec::new())
    }

    /// See [`Tool::describe`]. Receives the raw arguments, which may not deserialize.
    fn describe(&self, args: &Value, env: &dyn Environment) -> ToolDescription {
        let _ = (args, env);
        ToolDescription::named(Self::NAME)
    }

    /// See [`Tool::prepare_arguments`]. Runs before deserialization.
    fn prepare_arguments(&self, args: Value) -> Value {
        args
    }

    /// See [`Tool::check`]. Arguments that do not deserialize fail before this is called.
    async fn check(&self, args: &Self::Args, ctx: &ToolCtx) -> Result<(), ToolError> {
        let _ = (args, ctx);
        Ok(())
    }

    async fn run(&self, args: Self::Args, ctx: ToolCtx) -> Result<ToolOutput, ToolError>;
}

/// Adapts a [`TypedTool`] to [`Tool`]. The spec is generated once.
pub struct Typed<T: TypedTool> {
    inner: T,
    spec: ToolSpec,
}

impl<T: TypedTool> Typed<T> {
    pub fn new(inner: T) -> Self {
        let spec = ToolSpec {
            name: T::NAME.to_string(),
            description: T::DESCRIPTION.to_string(),
            parameters: parameters_schema::<T::Args>(),
        };
        Self { inner, spec }
    }

    /// The same tool without one of its parameters in the spec: for a parameter that means
    /// nothing in some sessions. Arguments that still give it parse as before.
    pub fn without_parameter(mut self, name: &str) -> Self {
        if let Some(properties) = self.spec.parameters.get_mut("properties").and_then(Value::as_object_mut) {
            properties.remove(name);
        }
        if let Some(required) = self.spec.parameters.get_mut("required").and_then(Value::as_array_mut) {
            required.retain(|r| r != name);
        }
        self
    }
}

#[async_trait]
impl<T: TypedTool> Tool for Typed<T> {
    fn spec(&self) -> &ToolSpec {
        &self.spec
    }

    fn kind(&self) -> ToolKind {
        self.inner.kind()
    }

    fn kind_for(&self, args: &Value) -> ToolKind {
        self.inner.kind_for(args)
    }

    fn guidelines(&self) -> &[&'static str] {
        self.inner.guidelines()
    }

    async fn authorities(&self, args: &Value, ctx: &ToolCtx) -> Result<Vec<Authority>, ToolError> {
        let args = parse_args::<T>(args.clone())?;
        self.inner.authorities(&args, ctx).await
    }

    fn describe(&self, args: &Value, env: &dyn Environment) -> ToolDescription {
        self.inner.describe(args, env)
    }

    fn prepare_arguments(&self, args: Value) -> Value {
        self.inner.prepare_arguments(args)
    }

    async fn check(&self, args: &Value, ctx: &ToolCtx) -> Result<(), ToolError> {
        let args = parse_args::<T>(args.clone())?;
        self.inner.check(&args, ctx).await
    }

    async fn execute(&self, args: Value, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let args = parse_args::<T>(args)?;
        self.inner.run(args, ctx).await
    }
}

fn parse_args<T: TypedTool>(args: Value) -> Result<T::Args, ToolError> {
    serde_json::from_value(args).map_err(|e| ToolError::InvalidArgs(e.to_string()))
}

/// JSON Schema for tool parameters, cleaned up for model APIs: no `$ref`, `$schema`, `title`
/// or `format`; optional fields are simply not required; top level is a closed object.
pub fn parameters_schema<T: JsonSchema>() -> Value {
    let settings = schemars::r#gen::SchemaSettings::draft07().with(|s| {
        s.option_nullable = false;
        s.option_add_null_type = false;
        s.inline_subschemas = true;
    });
    let root = settings.into_generator().into_root_schema_for::<T>();
    let mut schema = serde_json::to_value(&root).unwrap_or_else(|_| json!({}));
    clean_schema(&mut schema);
    if let Value::Object(map) = &mut schema {
        map.insert("type".into(), json!("object"));
        map.entry("properties").or_insert_with(|| json!({}));
        map.insert("additionalProperties".into(), json!(false));
    }
    schema
}

fn clean_schema(schema: &mut Value) {
    let Value::Object(map) = schema else {
        return;
    };
    for key in ["$schema", "title", "definitions", "format"] {
        map.remove(key);
    }
    if let Some(Value::Object(properties)) = map.get_mut("properties") {
        for property in properties.values_mut() {
            clean_schema(property);
        }
    }
    if let Some(items) = map.get_mut("items") {
        match items {
            Value::Array(list) => list.iter_mut().for_each(clean_schema),
            other => clean_schema(other),
        }
    }
    for key in ["anyOf", "oneOf", "allOf"] {
        if let Some(Value::Array(list)) = map.get_mut(key) {
            list.iter_mut().for_each(clean_schema);
        }
    }
    if let Some(additional) = map.get_mut("additionalProperties")
        && additional.is_object()
    {
        clean_schema(additional);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    #[derive(Deserialize, JsonSchema)]
    #[allow(dead_code)]
    struct Item {
        /// The text.
        text: String,
    }

    #[derive(Deserialize, JsonSchema)]
    #[allow(dead_code)]
    struct Args {
        /// A path.
        path: String,
        /// Optional count.
        #[serde(default)]
        count: Option<usize>,
        items: Vec<Item>,
    }

    #[test]
    fn schema_is_clean_and_inlined() {
        let schema = parameters_schema::<Args>();
        assert_eq!(schema["type"], "object");
        assert_eq!(schema["additionalProperties"], false);
        assert!(schema.get("$schema").is_none());
        assert!(schema.get("title").is_none());
        assert!(schema.get("definitions").is_none());
        assert_eq!(schema["properties"]["path"]["description"], "A path.");
        assert_eq!(schema["properties"]["count"]["type"], "integer");
        assert!(schema["properties"]["count"].get("format").is_none());
        assert_eq!(schema["properties"]["items"]["items"]["properties"]["text"]["type"], "string");
        let required: Vec<&str> = schema["required"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| v.as_str().unwrap())
            .collect();
        assert!(required.contains(&"path"));
        assert!(!required.contains(&"count"));
    }
}
