import json
from pathlib import Path
import tempfile
import unittest
from types import SimpleNamespace
from her_agent import HerAgent
from harbor.models.agent.context import AgentContext

class FakeEnvironment:
    default_user = None
    def __init__(self, fail=False):
        self.commands, self.files, self.fail = [], {}, fail
    async def upload_file(self, source, target):
        self.files[target] = Path(source).read_bytes()
    async def download_file(self, source, target):
        Path(target).write_bytes(self.files[source])
    async def exec(self, command, **kwargs):
        self.commands.append((command, kwargs))
        if '/her exec' in command and self.fail:
            raise RuntimeError('simulated execution failure')
        return SimpleNamespace(return_code=0, stdout='', stderr='')

class AdapterTests(unittest.IsolatedAsyncioTestCase):
    async def test_prompt_is_data_and_cwd_is_preserved(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            (root/'her').touch()
            (root/'models.toml').write_text('')
            agent = HerAgent(logs_dir=root, binary_path=root/'her', models_path=root/'models.toml', model_name='mock/test', skip_model_parsing=True)
            env = FakeEnvironment()
            prompt = '$(touch /tmp/injected) `echo bad` "quotes"\n第二行'
            await agent.run(prompt, env, AgentContext())
            self.assertEqual(env.files['/installed-agent/instruction.txt'].decode(), prompt)
            command, opts = next((c, o) for c, o in env.commands if '/her exec' in c)
            self.assertNotIn(prompt, command)
            self.assertIsNone(opts['cwd'])
            self.assertIn('--permission-mode bypass-permissions', command)

    async def test_failure_saves_rotation_and_releases_lock(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            (root/'her').touch()
            auth = root/'auth'
            conn = auth/'connections/chatgpt'
            conn.mkdir(parents=True)
            (conn/'profile.json').write_text('{}')
            (conn/'tokens.toml').write_text('token="old"')
            agent = HerAgent(logs_dir=root, binary_path=root/'her', auth_home=auth, model_name='local-chatgpt-api-test/m', skip_model_parsing=True)
            class RotatingEnvironment(FakeEnvironment):
                async def exec(self, command, **kwargs):
                    if '/her exec' in command:
                        self.files[str(conn/'tokens.toml')] = b'token="rotated"'
                    return await super().exec(command, **kwargs)
            with self.assertRaisesRegex(RuntimeError, 'simulated'):
                await agent.run('task', RotatingEnvironment(fail=True), AgentContext())
            self.assertEqual((conn/'tokens.toml').read_text(), 'token="rotated"')
            await agent.run('second task', FakeEnvironment(), AgentContext())

    def test_usage_includes_cache_and_children_without_fake_price(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            (root/'her').touch()
            (root/'models.toml').write_text('')
            agent = HerAgent(logs_dir=root, binary_path=root/'her', models_path=root/'models.toml', model_name='mock/test', skip_model_parsing=True)
            (root/'her-result.json').write_text(json.dumps({'usage': {'input_tokens':10,'cache_read_tokens':20,'output_tokens':3}, 'agents':[{'usage':{'input_tokens':5,'cache_write_tokens':2,'output_tokens':7}}]}))
            ctx = AgentContext()
            agent.populate_context_post_run(ctx)
            self.assertEqual((ctx.n_input_tokens,ctx.n_cache_tokens,ctx.n_output_tokens),(37,20,10))
            self.assertIsNone(ctx.cost_usd)

if __name__ == '__main__':
    unittest.main()
