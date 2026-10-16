"""Exercise the production Compose configuration without reading user .env files."""
import json
import os
from pathlib import Path
import subprocess
import shutil
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ProductionConfigTest(unittest.TestCase):
    def config(self, selected="", files=None, directory=ROOT, overrides=None):
        env = {key: value for key, value in os.environ.items() if not key.startswith("WB2A_")}
        env.pop("COMPOSE_FILE", None)
        env.pop("COMPOSE_PROJECT_NAME", None)
        env.update(WB2A_CONFIG_FILE=selected, WB2A_API_KEY="mock-shared-api")
        env.update(overrides or {})
        args = ["docker", "compose", "--env-file", str(ROOT / "deploy/acceptance.env")]
        for file in files or [directory / "docker-compose.yml"]:
            args.extend(["-f", str(file)])
        result = subprocess.run(
            [*args, "config", "--format", "json"], cwd=directory,
            env=env, capture_output=True, text=True, check=True)
        return json.loads(result.stdout)

    def test_runtime_needs_only_compose_and_never_builds_or_mounts_source(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            shutil.copy2(ROOT / "docker-compose.yml", directory)
            config = self.config(directory=directory)
        self.assertEqual(set(config["services"]), {"core", "console"})
        for name in ("core", "console"):
            service = config["services"][name]
            self.assertNotIn("build", service, "runtime must not build images")
            tag = "core" if name == "core" else "webui"
            self.assertRegex(service["image"], rf"^registry\.cn-hangzhou\.aliyuncs\.com/cateyes/go:wb2api-{tag}-[1-9][0-9]{{9}}$")
            self.assertEqual(service["platform"], "linux/amd64")
            for mount in service["volumes"]:
                self.assertEqual(mount["type"], "bind")
                self.assertTrue(
                    mount["source"].replace("\\", "/").startswith(
                        str(directory / "runtime/wb2api").replace("\\", "/") + "/"))
            self.assertNotIn("healthcheck", service)
            self.assertEqual(service["environment"], {"TZ": "Asia/Shanghai"})
        self.assertNotIn("ports", config["services"]["core"])
        self.assertEqual(len(config["services"]["console"]["ports"]), 1)
        self.assertEqual(config["services"]["core"]["image"].rsplit("-", 1)[1],
                         config["services"]["console"]["image"].rsplit("-", 1)[1])

        self.assertEqual(config["services"]["console"]["depends_on"]["core"]["condition"], "service_started")
        self.assertNotIn("depends_on", config["services"]["core"])
        self.assertTrue(config["services"]["console"]["volumes"][0]["read_only"])

    def test_runtime_ignores_host_environment_without_explicit_yaml_configuration(self):
        baseline = self.config()
        config = self.config(overrides={"WB2A_VERSION": "1789519600", "TZ": "UTC",
                                      "WB2A_PORT": "19999", "WB2A_BIND_ADDRESS": "127.0.0.2",
                                      "WB2A_AUTHS_VOLUME": "unwanted", "WB2A_CONFIG_FILE": "/unwanted.json",
                                      "WB2A_PUBLIC_ORIGIN": "https://unwanted.test",
                                      "WB2A_CORE_IMAGE": "other/core:old",
                                      "WB2A_CONSOLE_IMAGE": "other/console:new",
                                      "WB2A_ADMIN_KEY": "unexpected-admin",
                                      "WB2A_API_KEY": "unexpected-api"})
        self.assertEqual(config, baseline, "host environment must not alter deployment")

    def test_commented_keys_can_be_configured_directly_in_yaml(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            text = (ROOT / "docker-compose.yml").read_text()
            text = text.replace('# WB2A_ADMIN_KEY: ""', 'WB2A_ADMIN_KEY: "' + "c" * 32 + '"')
            text = text.replace('# WB2A_API_KEY: ""', 'WB2A_API_KEY: "fixture-api"')
            (directory / "docker-compose.yml").write_text(text)
            config = self.config(directory=directory)
        for service in config["services"].values():
            self.assertEqual(service["environment"], {"TZ": "Asia/Shanghai",
                             "WB2A_ADMIN_KEY": "c" * 32, "WB2A_API_KEY": "fixture-api"})

    def test_source_build_entry_preserves_config_and_runtime_topology(self):
        build_file = ROOT / "docker-compose.build.yaml"
        self.assertTrue(build_file.is_file(), "source build entry missing")
        for selected in ("", str(ROOT / "deploy/acceptance-config.json")):
            with self.subTest(selected=selected):
                config = self.config(selected, files=[build_file])
                self.assertEqual(set(config["services"]), {"core", "console"})
                for name in ("core", "console"):
                    self.assertEqual(config["services"][name]["build"]["dockerfile"], f"deploy/{name}.Dockerfile")
                    self.assertTrue(config["services"][name]["build"].get("pull"), "build must resolve target-platform base")
                mounts = [m for m in config["services"]["core"]["volumes"] if m["target"] == "/app/config.json"]
                self.assertEqual(len(mounts), 1, "production config selection is missing")
                mount = mounts[0]
                self.assertEqual(mount["source"], str(ROOT / "deploy/default-config.json"))
                self.assertTrue(Path(mount["source"]).is_file())
                self.assertTrue(mount["read_only"])
                self.assertFalse(mount.get("bind", {}).get("create_host_path", False))
                for service in ("core", "console"):
                    self.assertEqual(config["services"][service]["environment"], {"TZ": "Asia/Shanghai"})

    def test_qoder_sidecar_pins_cli_config_dir_to_the_mounted_credential_volume(self):
        """qoderclicn resolves ~/.qoder-cn by default while the credential volume is mounted at
        /root/.qoderworkcn, so an unpinned sidecar writes and reads credentials in two different
        directories. That surfaces as the recurring 'Not logged in' channel outage."""
        override = ROOT / "deploy/compose.qoder.yml"
        self.assertTrue(override.is_file(), "qoder sidecar entry missing")
        config = self.config(files=[ROOT / "docker-compose.yml", override])
        service = config["services"]["qoder-proxy"]
        targets = {mount["target"] for mount in service["volumes"]}
        self.assertEqual(targets, {"/root/.qoderworkcn"}, "credential volume mount target changed")
        self.assertEqual(
            service["environment"].get("QODERCN_CONFIG_DIR"),
            "/root/.qoderworkcn",
            "the qoder CLI must be pinned to the mounted credential directory",
        )

    def test_optional_runtime_config_is_explicit_and_readonly(self):
        override = ROOT / "deploy/compose.config.yml"
        self.assertTrue(override.is_file(), "explicit config entry missing")
        config = self.config(str(ROOT / "deploy/acceptance-config.json"),
                             files=[ROOT / "docker-compose.yml", override])
        mounts = [m for m in config["services"]["core"]["volumes"] if m["target"] == "/app/config.json"]
        self.assertEqual(len(mounts), 1)
        self.assertTrue(mounts[0]["read_only"])
        self.assertFalse(mounts[0].get("bind", {}).get("create_host_path", False))
        self.assertEqual(mounts[0]["source"], str(ROOT / "runtime/wb2api/config.json"))
        self.assertEqual(config, self.config(files=[ROOT / "docker-compose.yml", override]))


    def test_freellm_sidecar_joins_console_namespace_and_keeps_db_in_mounted_volume(self):
        """FreeLLMAPI 生成统一 key 的 SQLite 落在 /data；sidecar 借 console 的网络命名空间，
        数据面只监听回环。key 由 sidecar 首启生成后回填 .env，本仓不落供应商密钥文件。"""
        override = ROOT / "deploy/compose.freellm.yml"
        self.assertTrue(override.is_file(), "freellm sidecar entry missing")
        config = self.config(files=[ROOT / "docker-compose.yml", override])
        service = config["services"]["freellmapi"]
        self.assertEqual(service["network_mode"], "service:console")
        self.assertEqual(service["environment"]["FREEAPI_DB_PATH"], "/data/freeapi.db")
        self.assertEqual({m["target"] for m in service["volumes"]}, {"/data"})
        console_env = config["services"]["console"]["environment"]
        self.assertEqual(console_env["WB2A_FREELLMAPI_URL"], "http://127.0.0.1:7868")
        # console 只认统一 key，绝不持有供应商 key。
        self.assertEqual(console_env["WB2A_FREELLMAPI_KEY"], "")
        for key in console_env:
            self.assertNotIn("GEMINI", key)
            self.assertNotIn("GROQ", key)
            self.assertNotIn("OPENROUTER", key)

    def test_freellm_sidecar_rate_limit_is_widened_for_a_single_gateway_bucket(self):
        """sidecar 的 PROXY_RATE_LIMIT_RPM 默认 120/分钟，按直连对端分桶。
        console 一律剥掉 x-forwarded-*（防伪造），且旁路共享 console 的网络命名空间，
        于是棱镜的全部流量在 sidecar 眼里就是同一个回环客户端——沿用默认值会让整条
        通道被一个客户端桶钉死，每 30 秒一轮的探活也算在里面。这里显式抬到远高于
        默认值的档位（真闸仍是池内每供应商的日帽），并把理由写在 compose 注释里。"""
        config = self.config(files=[ROOT / "docker-compose.yml", ROOT / "deploy/compose.freellm.yml"])
        env = config["services"]["freellmapi"]["environment"]
        self.assertIn("PROXY_RATE_LIMIT_RPM", env, "分钟闸保持默认 120 会把整条通道塌成一个桶")
        self.assertGreater(int(env["PROXY_RATE_LIMIT_RPM"]), 120)


    def test_traeai_sidecar_is_the_same_image_on_the_international_edition(self):
        """Trae 国际版与国内版共用同一个 trae2api 镜像，只靠 TRAE_EDITION 换上游区域。
        这里钉三件实测踩过/容易静默失效的事：
          1. sidecar 必须收到**二进制真正读的名字** TRAE_API_KEY。国内版 overlay 只注入了
             WB2A_TRAE_KEY，于是 TRAE_API_KEY 为空 → withAuth 退化成"不鉴权"，
             从 git status 上完全看不出来。
          2. 端口与数据目录必须与国内版分开（7869/runtime/traeai vs 7866/runtime/trae），
             否则两只容器会争同一份账号池与同一个回环端口。
          3. 与国内版一样借 console 的网络命名空间、只绑回环，凭据目录是唯一挂载点。
        """
        override = ROOT / "deploy/compose.traeai.yml"
        self.assertTrue(override.is_file(), "traeai sidecar entry missing")
        config = self.config(files=[ROOT / "docker-compose.yml", override])
        service = config["services"]["traeai-proxy"]
        self.assertEqual(service["network_mode"], "service:console")
        env = service["environment"]
        self.assertEqual(env["TRAE_EDITION"], "intl")
        self.assertEqual(env["TRAE_LISTEN"], "127.0.0.1:7869")
        self.assertEqual(env.get("TRAE_CALLBACK_LISTEN"), "0")
        self.assertEqual(env["TRAE_API_KEY"], "", "sidecar 必须收到 TRAE_API_KEY（空值也显式存在）")
        self.assertEqual({m["target"] for m in service["volumes"]}, {"/data"})
        source = {m["source"] for m in service["volumes"]}
        # compose 在 Windows 上把宿主路径解析成反斜杠形式，判据先归一分隔符再比，
        # 否则会得到一条"看着像挂载错了、其实是判据自己写死斜杠"的假红。
        flat = {s.replace("\\", "/") for s in source}
        self.assertTrue(any(flat_path.endswith("/runtime/traeai") for flat_path in flat),
                        f"凭据目录未挂到 runtime/traeai: {source}")
        # 与国内版分流：数据目录和 console 侧上游地址都不能撞上 deploy/compose.trae.yml
        # （它自己不设 TRAE_LISTEN，靠镜像 ENV 的 127.0.0.1:7866 兜底，所以这里比 console 的 URL）。
        cn = self.config(files=[ROOT / "docker-compose.yml", ROOT / "deploy/compose.trae.yml"])
        cn_sources = {m["source"].replace("\\", "/") for m in cn["services"]["trae-proxy"]["volumes"]}
        self.assertTrue(any(s.endswith("/runtime/trae") for s in cn_sources), f"国内版挂载意外: {cn_sources}")
        self.assertFalse(flat & cn_sources, "两只 sidecar 挂到了同一个凭据目录")
        console_env = config["services"]["console"]["environment"]
        cn_console_env = cn["services"]["console"]["environment"]
        self.assertEqual(console_env["WB2A_TRAEAI_URL"], "http://127.0.0.1:7869")
        self.assertEqual(cn_console_env["WB2A_TRAE_URL"], "http://127.0.0.1:7866")
        self.assertNotEqual(console_env["WB2A_TRAEAI_URL"], cn_console_env["WB2A_TRAE_URL"])


if __name__ == "__main__":
    unittest.main()
