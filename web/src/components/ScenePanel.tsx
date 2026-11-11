// 模拟数据的场景切换面板。只在模拟模式下渲染，正式构建里不会出现。
import { getScene, setScene } from "../mocks/server";
import { SCENES } from "../mocks/data";

export default function ScenePanel() {
  const cur = getScene();
  return (
    <details className="scene">
      <summary>模拟场景：{SCENES.find(([k]) => k === cur)?.[1]}</summary>
      <div className="panel">
        {SCENES.map(([key, name, desc]) => (
          <button key={key} className={key === cur ? "on" : ""} onClick={() => setScene(key)} title={desc}>
            {name}
            <div className="sub">{desc}</div>
          </button>
        ))}
      </div>
    </details>
  );
}
