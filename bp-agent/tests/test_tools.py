from bp_agent.tools import ToolRegistry, ToolSchema


def test_register_and_execute():
    registry = ToolRegistry()

    def add(a: int, b: int) -> int:
        return a + b

    schema = ToolSchema(
        name="add",
        description="Add two numbers",
        parameters={
            "type": "object",
            "properties": {
                "a": {"type": "integer"},
                "b": {"type": "integer"},
            },
            "required": ["a", "b"],
        },
    )

    registry.register("add", add, schema)
    result = registry.execute("add", {"a": 2, "b": 3})

    assert result.success is True
    assert result.output == 5


def test_execute_not_found():
    registry = ToolRegistry()
    result = registry.execute("nonexistent", {})

    assert result.success is False
    assert "not found" in result.error


def test_execute_with_error():
    registry = ToolRegistry()

    def failing_tool():
        raise ValueError("Something went wrong")

    registry.register("fail", failing_tool, ToolSchema("fail", "Fails"))
    result = registry.execute("fail", {})

    assert result.success is False
    assert "Something went wrong" in result.error


def test_get_schemas():
    registry = ToolRegistry()
    registry.register("a", lambda: 1, ToolSchema("a", "Tool A"))
    registry.register("b", lambda: 2, ToolSchema("b", "Tool B"))

    schemas = registry.get_schemas()
    assert len(schemas) == 2
    names = [s.name for s in schemas]
    assert "a" in names
    assert "b" in names
