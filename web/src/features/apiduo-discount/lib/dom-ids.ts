/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * 规则行与生效面板共用同一份候选 datalist。
 *
 * datalist 挂在编辑器根节点，面板只需要按 id 引用；把 id 放在这里，
 * 两个组件就不必互相 import。
 */
export const GROUP_LIST_ID = 'apiduo-discount-groups'
export const MODEL_LIST_ID = 'apiduo-discount-models'
