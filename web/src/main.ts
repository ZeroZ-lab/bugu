import "./App";
import { lang, setLang } from "./i18n";
import { applyTheme, getTheme } from "./theme";
import "./style.css";

// 第一屏按上次用的语言(或浏览器的语言)显示,连上后台后再按设置调整。
// 页面里的 bugu-app 元素在 App 模块登记时就开始渲染,语言和外观在 theme、i18n 模块加载时已经可用;
// 这里补上 html 的 lang 属性和已保存的外观。
setLang(lang());
applyTheme(getTheme());
